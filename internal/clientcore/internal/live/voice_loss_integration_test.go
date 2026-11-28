package client

import (
	"context"
	"net"
	"testing"
	"time"
	voice "uniclog.io/sonoryx/apps/server/testsupport/voice"
	"uniclog.io/sonoryx/internal/audio"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/protocol/protocoltest"
	"uniclog.io/sonoryx/internal/transport/udp"
	"uniclog.io/sonoryx/internal/voiceflow"
)

type loseDownstreamCodec struct{ inner protocol.DatagramCodec }

func (c loseDownstreamCodec) Encode(ctx protocol.DatagramContext, p protocol.VoicePacket) ([]byte, error) {
	data, err := c.inner.Encode(ctx, p)
	if err != nil {
		return nil, err
	}
	if p.Type == protocol.PacketVoiceBundleV2 {
		b, err := protocol.DecodeVoiceBundleV2Packet(p, false)
		if err == nil && b.DatagramSequence == 3 {
			return []byte{}, nil
		}
	}
	return data, nil
}
func (c loseDownstreamCodec) Decode(ctx protocol.DatagramContext, data []byte) (protocol.VoicePacket, error) {
	return c.inner.Decode(ctx, data)
}

// The uplink drops primary #2; its next bundle restores it at the server.
// The downstream drops datagram #3; the next bundle restores frame #4.
// Socket encryption, real Opus and the final epoch gate stay in the path.
func TestVoiceRecoversAcrossBothHopsWithoutDuplicatePlayback(t *testing.T) {
	serverRaw, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer serverRaw.Close()
	clientRaw, err := net.DialUDP("udp4", nil, serverRaw.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer clientRaw.Close()
	serverCodec, clientCodec := protocoltest.Pair(t, 42)
	server, err := udp.NewServerPacketConn(serverRaw, loseDownstreamCodec{serverCodec})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := udp.NewClientPacketConn(clientRaw, clientCodec)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.BindSession(42); err != nil {
		t.Fatal(err)
	}
	h := voice.NewHub()
	sourceAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 19001}
	h.Add(&voice.Session{ID: 73, Name: "source", Addr: sourceAddr})
	h.Add(&voice.Session{ID: 42, Name: "listener", Addr: clientRaw.LocalAddr().(*net.UDPAddr)})
	for _, id := range []uint64{73, 42} {
		if err := h.JoinChannel(id, voice.DefaultChannelID); err != nil {
			t.Fatal(err)
		}
	}
	h.SetVoiceBundles(true)
	h.SetVoiceUplink(true)
	for _, id := range []uint64{73, 42} {
		if err := h.RequestVoiceState(id, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	var installed *protocol.VoiceStateSnapshot
	for _, delivery := range h.DueVoiceStates(time.Now()) {
		var assembler protocol.VoiceStateAssembler
		for _, p := range delivery.Packets {
			page, err := protocol.DecodeVoiceState(p.Payload)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, _, err := assembler.Add(page, 0, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			if snapshot != nil {
				if delivery.Session.ID == 42 {
					installed = snapshot
				}
				if !h.AckVoiceState(delivery.Session.ID, snapshot.Header.Revision) {
					t.Fatal("state ack failed")
				}
			}
		}
	}
	if installed == nil {
		t.Fatal("listener state missing")
	}
	s := NewState(42, "listener")
	s.SetChannelID(voice.DefaultChannelID)
	s.ReplaceSnapshot(h.ClientSnapshot())
	if err := s.installVoiceState(s.Generation(), installed, func(domain.AudioProfile) error { return nil }); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	encoded := make(chan audio.MediaFrame, 8)
	control := make(chan protocol.VoicePacket, 8)
	done := make(chan error, 1)
	go func() { done <- ReceiveLoop(ctx, conn, encoded, control, s) }()
	config := audio.CodecConfig{SampleRate: 48000, Channels: 1, SamplesPerFrame: 960, Bitrate: 24000, Application: domain.OpusApplicationVoIP}
	encoder, err := audio.NewOpusEncoder(config)
	if err != nil {
		t.Fatal(err)
	}
	source, _ := h.Get(73)
	var history voiceflow.Window
	start := time.Now()
	sourceTelemetry := voiceflow.NewEndpoint(protocol.VoiceUpstream, start)
	sourceTelemetry.SetEnabled(true)
	for seq := uint32(1); seq <= 6; seq++ {
		samples := make([]int16, 960)
		for i := range samples {
			samples[i] = int16((i % 40) * 100)
		}
		payload, err := encoder.Encode(samples)
		if err != nil {
			t.Fatal(err)
		}
		primary := protocol.VoiceBundleFrame{Sequence: seq, Payload: payload}
		if _, err := history.Accept([]protocol.VoiceBundleFrame{primary}, start.Add(time.Duration(seq)*20*time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		packet, err := protocol.NewVoiceBundleV2Packet(73, protocol.VoiceBundleV2{DatagramSequence: seq, SourceEpoch: source.SourceEpoch, Frames: history.ForFrame(primary, 2, start.Add(time.Duration(seq)*20*time.Millisecond))}, true)
		if err != nil {
			t.Fatal(err)
		}
		sourceTelemetry.Sent(seq, true, true, time.Now())
		if seq == 2 {
			continue
		}
		if err := voice.HandleVoicePacket(server, h, packet, sourceAddr); err != nil {
			t.Fatal(err)
		}
	}
	arrivals := make(map[int]audio.MediaFrame)
	for range 4 {
		select {
		case packet := <-encoded:
			frames := packet.Batch
			if len(frames) == 0 {
				frames = []audio.MediaFrame{packet}
			}
			arrivals[int(frames[len(frames)-1].Sequence-1)] = packet
		case <-time.After(2 * time.Second):
			t.Fatal("missing relayed packet")
		}
	}
	buffers := newStreamJitterBuffers(3)
	endpoint := s.VoiceEndpoint()
	buffers.onMetric = func(seq, restored, late uint32) { endpoint.Frame(seq, 0, restored); endpoint.Frame(seq, 1, late) }
	decoders := newStreamDecoders(func() (audio.Decoder, error) { return audio.NewOpusDecoder(config) })
	origin := time.Unix(100, 0)
	var sequences []uint32
	writes := 0
	for tick := 0; tick < 10; tick++ {
		now := origin.Add(time.Duration(tick) * 20 * time.Millisecond)
		if packet, ok := arrivals[tick]; ok {
			if len(packet.Batch) > 0 {
				for i := range packet.Batch {
					packet.Batch[i].ArrivedAt = now
				}
			} else {
				packet.ArrivedAt = now
			}
			buffers.PushAt(packet, now)
		}
		for _, frame := range buffers.TickAt(now) {
			if frame.Missing {
				t.Fatal("timely repair became PLC", frame.Sequence)
			}
			pcm, err := decoders.decode(frame)
			if err != nil {
				t.Fatal(err)
			}
			_, pcm.PlaybackEpoch = s.Audio.PlaybackSnapshot()
			mixed := mixPCMFrames([]audio.MediaPCMFrame{pcm})
			if err := s.playVoicePCM(mixed, func() error { writes++; return nil }); err != nil {
				t.Fatal(err)
			}
			sequences = append(sequences, frame.Sequence)
		}
	}
	if len(sequences) != 6 || writes != 6 {
		t.Fatalf("playback sequences=%v writes=%d", sequences, writes)
	}
	for i, seq := range sequences {
		if seq != uint32(i+1) {
			t.Fatal("reordered or duplicate playback", sequences)
		}
	}
	// Transport loss remains visible even though every source frame played.
	future := time.Now().Add(2 * time.Second)
	upSummary, _ := sourceTelemetry.Tick(future)
	upPayload, err := protocol.EncodeVoiceSummary(*upSummary)
	if err != nil {
		t.Fatal(err)
	}
	if err := voice.HandleVoiceTelemetry(h, protocol.VoicePacket{Type: protocol.PacketVoiceSummary, SessionID: 73, Payload: upPayload}, sourceAddr, future); err != nil {
		t.Fatal(err)
	}
	for _, delivery := range h.DueVoiceTelemetry(future) {
		for _, packet := range delivery.Packets {
			if delivery.Session.ID == 42 {
				if err := s.AcceptVoiceTelemetry(s.Generation(), packet, future); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	_, downReport := endpoint.Tick(future.Add(100 * time.Millisecond))
	if downReport == nil {
		t.Fatal("downstream report missing")
	}
	if got := endpoint.Stats(future.Add(200 * time.Millisecond)); !got.Incoming.Known || got.Incoming.Loss != 20 || got.Incoming.Restored != 2 {
		t.Fatalf("downstream stats: %+v", got.Incoming)
	}
	var upReport *protocol.VoiceReport
	for _, delivery := range h.DueVoiceTelemetry(future.Add(100 * time.Millisecond)) {
		for _, packet := range delivery.Packets {
			if delivery.Session.ID == 73 && packet.Type == protocol.PacketVoiceReport {
				r, err := protocol.DecodeVoiceReport(packet.Payload)
				if err != nil {
					t.Fatal(err)
				}
				upReport = &r
			}
		}
	}
	if upReport == nil {
		t.Fatal("upstream report missing")
	}
	if err := sourceTelemetry.AcceptReport(*upReport, future.Add(200*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	up := sourceTelemetry.Stats(future.Add(200 * time.Millisecond)).Outgoing
	if !up.Known || up.Loss < 16 || up.Loss > 17 || up.Restored != 1 {
		t.Fatalf("upstream stats %+v", up)
	}
	cancel()
	conn.SetReadDeadline(time.Now())
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("receive loop did not stop")
	}
}
