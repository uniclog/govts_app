package voice

import (
	"errors"
	"net"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
	"uniclog.io/sonoryx/internal/voiceflow"
)

type voiceWrite struct {
	recipient uint64
	bundle    protocol.VoiceBundleV2
}
type voiceCaptureCodec struct {
	hub      *Hub
	writes   []voiceWrite
	failedID uint64
}

func (c *voiceCaptureCodec) Encode(ctx protocol.DatagramContext, p protocol.VoicePacket) ([]byte, error) {
	// This read would deadlock if the server held Hub.mu during network work.
	_, _ = c.hub.Get(ctx.KeyOwnerID)
	b, err := protocol.DecodeVoiceBundleV2Packet(p, false)
	if err != nil {
		return nil, err
	}
	c.writes = append(c.writes, voiceWrite{ctx.KeyOwnerID, b})
	if ctx.KeyOwnerID == c.failedID {
		return nil, errors.New("recipient unavailable")
	}
	return []byte{1}, nil
}
func (*voiceCaptureCodec) Decode(protocol.DatagramContext, []byte) (protocol.VoicePacket, error) {
	return protocol.VoicePacket{}, errors.New("unused")
}

func mediaServerFixture(t *testing.T) (*Hub, *udp.ServerPacketConn, *voiceCaptureCodec, Session, Session) {
	t.Helper()
	h := NewHub()
	speaker, listener := joinedVoiceSession(t, h, "speaker"), joinedVoiceSession(t, h, "listener")
	codec := &voiceCaptureCodec{hub: h}
	raw, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := udp.NewServerPacketConn(raw, codec)
	if err != nil {
		raw.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return h, conn, codec, speaker, listener
}
func ackMediaState(t *testing.T, h *Hub, id uint64) {
	t.Helper()
	now := time.Now()
	if err := h.RequestVoiceState(id, now); err != nil {
		t.Fatal(err)
	}
	for _, delivery := range h.DueVoiceStates(now) {
		page, err := protocol.DecodeVoiceState(delivery.Packets[0].Payload)
		if err != nil || !h.AckVoiceState(delivery.Session.ID, page.Revision) {
			t.Fatal("cannot acknowledge voice state", err)
		}
	}
}
func upstreamMedia(t *testing.T, s Session, datagram uint32, frames ...protocol.VoiceBundleFrame) protocol.VoicePacket {
	t.Helper()
	p, err := protocol.NewVoiceBundleV2Packet(s.ID, protocol.VoiceBundleV2{DatagramSequence: datagram, SourceEpoch: s.SourceEpoch, Frames: frames}, true)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMediaV2RequiresAckRepairsOnceAndKeepsTransportCounter(t *testing.T) {
	h, conn, codec, speaker, listener := mediaServerFixture(t)
	frame := func(seq uint32) protocol.VoiceBundleFrame {
		return protocol.VoiceBundleFrame{Sequence: seq, Payload: []byte{byte(seq)}}
	}
	// No confirmed receiver state: no audio is sent.
	if err := HandleVoicePacket(conn, h, upstreamMedia(t, speaker, 1, frame(1)), speaker.Addr); err != nil {
		t.Fatal(err)
	}
	if len(codec.writes) != 0 {
		t.Fatal("unacknowledged receiver got voice")
	}
	ackMediaState(t, h, listener.ID)
	if err := HandleVoicePacket(conn, h, upstreamMedia(t, speaker, 2, frame(2), frame(3), frame(4)), speaker.Addr); err != nil {
		t.Fatal(err)
	}
	if len(codec.writes) != 3 {
		t.Fatalf("repair packets=%d, want primary + 2", len(codec.writes))
	}
	for i, w := range codec.writes {
		if w.bundle.DatagramSequence != uint32(i+1) || len(w.bundle.Frames) != 1 || w.bundle.Frames[0].Sequence != uint32(4-i) {
			t.Fatalf("repair order: %+v", codec.writes)
		}
	}
	if err := HandleVoicePacket(conn, h, upstreamMedia(t, speaker, 3, frame(2), frame(3), frame(4)), speaker.Addr); err != nil || len(codec.writes) != 3 {
		t.Fatal("duplicate caused retransmission", err)
	}
	if err := h.SetAudioState(speaker.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if err := h.SetAudioState(speaker.ID, false, false); err != nil {
		t.Fatal(err)
	}
	// Old epochs count as transport reception but never reach the audio cache.
	if err := HandleVoicePacket(conn, h, upstreamMedia(t, speaker, 4, frame(5)), speaker.Addr); err != nil || len(codec.writes) != 3 {
		t.Fatal("old source relayed", err)
	}
	speaker, _ = h.Get(speaker.ID)
	if h.sessions[speaker.ID].voiceFrames != nil {
		t.Fatal("old epoch repopulated history")
	}
	for _, delivery := range h.DueVoiceStates(time.Now()) {
		page, _ := protocol.DecodeVoiceState(delivery.Packets[0].Payload)
		h.AckVoiceState(delivery.Session.ID, page.Revision)
	}
	if err := HandleVoicePacket(conn, h, upstreamMedia(t, speaker, 5, frame(1)), speaker.Addr); err != nil {
		t.Fatal(err)
	}
	if len(codec.writes) != 4 || codec.writes[3].bundle.DatagramSequence != 4 || codec.writes[3].bundle.SourceEpoch != speaker.SourceEpoch {
		t.Fatal("epoch reset transport counter", codec.writes)
	}
}

func TestMediaV2RecipientFailureDoesNotBlockOthersOrReuseNumber(t *testing.T) {
	h, conn, codec, speaker, listener := mediaServerFixture(t)
	other := joinedVoiceSession(t, h, "other")
	ackMediaState(t, h, listener.ID)
	ackMediaState(t, h, other.ID)
	codec.failedID = listener.ID
	f := protocol.VoiceBundleFrame{Sequence: 1, Payload: []byte{1}}
	if err := HandleVoicePacket(conn, h, upstreamMedia(t, speaker, 1, f), speaker.Addr); err == nil {
		t.Fatal("write failure hidden")
	}
	if len(codec.writes) != 2 {
		t.Fatal("one recipient failure blocked another")
	}
	codec.failedID = 0
	f.Sequence = 2
	if err := HandleVoicePacket(conn, h, upstreamMedia(t, speaker, 2, f), speaker.Addr); err != nil {
		t.Fatal(err)
	}
	for _, w := range codec.writes[2:] {
		if w.bundle.DatagramSequence != 2 {
			t.Fatal("failed write reused transport sequence")
		}
	}
}

func TestMediaV2SizeFallbackAndReservedPackets(t *testing.T) {
	h, conn, codec, speaker, listener := mediaServerFixture(t)
	h.SetVoiceBundles(true)
	ackMediaState(t, h, listener.ID)
	for n := uint32(1); n <= 2; n++ {
		if err := HandleVoicePacket(conn, h, upstreamMedia(t, speaker, n, protocol.VoiceBundleFrame{Sequence: n, Payload: make([]byte, 1180)}), speaker.Addr); err != nil {
			t.Fatal(err)
		}
	}
	if len(codec.writes) != 2 || len(codec.writes[1].bundle.Frames) != 1 {
		t.Fatal("oversized copy did not fall back to v2 single frame")
	}
	for _, kind := range []uint8{protocol.PacketVoice, protocol.PacketVoiceBundle} {
		if err := HandlePacket(conn, h, NewRequestCache(), protocol.VoicePacket{Type: kind, SessionID: speaker.ID, Sequence: 3, Payload: []byte{1}}, speaker.Addr); err != nil {
			t.Fatal(err)
		}
	}
	if len(codec.writes) != 2 {
		t.Fatal("reserved v1 packet forwarded")
	}
}

func TestAdaptiveDownstreamModesAreIndependentPerRecipient(t *testing.T) {
	h, conn, codec, speaker, listener := mediaServerFixture(t)
	other := joinedVoiceSession(t, h, "other")
	h.SetVoiceBundles(true)
	ackMediaState(t, h, listener.ID)
	ackMediaState(t, h, other.ID)
	origin := time.Now().Add(-3 * time.Second)
	flow := voiceflow.NewEndpoint(protocol.VoiceDownstream, origin)
	flow.SetEnabled(true)
	var seq uint32
	for second := 0; second < 3; second++ {
		for i := 0; i < 50; i++ {
			seq++
			flow.Sent(seq, true, true, origin.Add(time.Duration(second)*time.Second+time.Duration(i)*20*time.Millisecond))
		}
		summary, _ := flow.Tick(origin.Add(time.Duration(second+1) * time.Second))
		var report protocol.VoiceReport
		report.Direction = protocol.VoiceDownstream
		for _, s := range summary.Intervals {
			report.Intervals = append(report.Intervals, protocol.VoiceReceiveInterval{ID: s.ID, FirstSequence: s.FirstSequence, LastSequence: s.LastSequence, ReceivedUnique: 45, Flags: protocol.VoiceIntervalComplete | protocol.VoiceBurstValid})
		}
		if err := flow.AcceptReport(report, origin.Add(time.Duration(second+1)*time.Second+100*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
	}
	if flow.Stats(time.Now()).Local.DesiredFrames != 3 {
		t.Fatal("training did not activate three frames")
	}
	h.mu.Lock()
	h.sessions[listener.ID].voiceFlow = flow
	h.sessions[listener.ID].voiceControl.lastDatagram = seq
	h.mu.Unlock()
	for n := uint32(1); n <= 3; n++ {
		if err := HandleVoicePacket(conn, h, upstreamMedia(t, speaker, n, protocol.VoiceBundleFrame{Sequence: n, Payload: []byte{byte(n)}}), speaker.Addr); err != nil {
			t.Fatal(err)
		}
	}
	counts := map[uint64]int{}
	for _, write := range codec.writes {
		if write.bundle.Frames[len(write.bundle.Frames)-1].Sequence == 3 {
			counts[write.recipient] = len(write.bundle.Frames)
		}
	}
	if counts[listener.ID] != 3 || counts[other.ID] != 2 {
		t.Fatalf("recipient modes were coupled: %v", counts)
	}
	current, _ := h.Get(speaker.ID)
	if current.SourceEpoch != speaker.SourceEpoch {
		t.Fatal("protection mode reset source epoch")
	}
}
