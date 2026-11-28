package client

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/audio"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/protocol/protocoltest"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func installedMediaFixture(t *testing.T) (*State, *protocol.VoiceStateSnapshot, audio.VoiceIdentity) {
	t.Helper()
	s, p := voiceClientFixture()
	if err := s.installVoiceState(s.Generation(), p, func(domain.AudioProfile) error { return nil }); err != nil {
		t.Fatal(err)
	}
	return s, p, audio.VoiceIdentity{Generation: s.Generation(), SourceEpoch: 1, ReceiverEpoch: 1}
}

func TestVoiceEpochPrunesOnlyAffectedQueuesAndChecksFinalPCM(t *testing.T) {
	s, p, identity := installedMediaFixture(t)
	// Keep a second, unaffected speaker in the same authoritative state.
	p.Sources = append(p.Sources, protocol.VoiceSourceState{SenderID: 7, SourceEpoch: 1, ChannelID: 1})
	p.Header.Revision++
	if err := s.installVoiceState(s.Generation(), p, nil); err != nil {
		t.Fatal(err)
	}
	b := newStreamJitterBuffers(3)
	d := newStreamDecoders(func() (audio.Decoder, error) {
		return decoderFunc(func([]byte) ([]int16, error) { return []int16{10}, nil }), nil
	})
	m := newPCMMixer()
	var decoded []audio.MediaPCMFrame
	for _, id := range []uint64{8, 7} {
		f := audio.MediaFrame{Voice: identity, SenderID: id, Sequence: 1, Data: []byte{1}, Duration: frameDuration}
		b.Push(f)
		pcm, err := d.decode(f)
		if err != nil {
			t.Fatal(err)
		}
		_, pcm.PlaybackEpoch = s.Audio.PlaybackSnapshot()
		m.Push(pcm)
		decoded = append(decoded, pcm)
	}
	queued := mixPCMFrames(decoded)
	if len(queued.Voices) != 2 {
		t.Fatal("mixed PCM lost source identities")
	}
	p.Header.Revision++
	p.Sources[1].SourceEpoch++
	if err := s.installVoiceState(s.Generation(), p, nil); err != nil {
		t.Fatal(err)
	}
	b.Prune(s)
	d.Prune(s)
	m.Prune(s)
	if len(b.bySender) != 1 || len(d.bySender) != 1 || len(m.bySender) != 1 || b.bySender[7].buffer == nil {
		t.Fatal("epoch reset affected wrong speakers")
	}
	writes := 0
	if err := s.playVoicePCM(queued, func() error { writes++; return nil }); err != nil || writes != 0 {
		t.Fatal("obsolete mixed PCM reached device", err)
	}
	current, _ := m.Mix()
	if err := s.playVoicePCM(current, func() error { writes++; return nil }); err != nil || writes != 1 {
		t.Fatal("unaffected speaker blocked", err)
	}
	p.Header.Revision++
	p.Header.OwnReceiverEpoch++
	p.Header.OwnControlRevision++
	if err := s.installVoiceState(s.Generation(), p, nil); err != nil {
		t.Fatal(err)
	}
	b.Prune(s)
	d.Prune(s)
	if len(b.bySender) != 0 || len(d.bySender) != 0 {
		t.Fatal("receiver epoch retained queues")
	}
	if err := s.playVoicePCM(current, func() error { writes++; return nil }); err != nil || writes != 1 {
		t.Fatal("old receiver PCM reached device")
	}
}

func TestVoiceBundleInsertedBeforeGapDecisionAndDecoderReset(t *testing.T) {
	identity := audio.VoiceIdentity{Generation: 1, SourceEpoch: 1, ReceiverEpoch: 1}
	b := newStreamJitterBuffers(1)
	f := func(seq uint32) audio.MediaFrame {
		return audio.MediaFrame{Voice: identity, SenderID: 8, Sequence: seq, Data: []byte{byte(seq)}}
	}
	now := time.Now()
	b.PushAt(f(1), now)
	b.PushAt(audio.MediaFrame{Batch: []audio.MediaFrame{f(2), f(3), f(4)}}, now.Add(20*time.Millisecond))
	var frames []audio.MediaFrame
	for i := 2; i <= 5; i++ {
		batch := b.TickAt(now.Add(time.Duration(i) * frameDuration))
		if len(batch) > 1 {
			t.Fatal("jitter emitted batch")
		}
		frames = append(frames, batch...)
	}
	if len(frames) != 4 {
		t.Fatalf("bundle emitted %d frames", len(frames))
	}
	for i, frame := range frames {
		if frame.Missing || frame.Sequence != uint32(i+1) {
			t.Fatal("premature PLC", frames)
		}
	}

	created := 0
	d := newStreamDecoders(func() (audio.Decoder, error) {
		created++
		return decoderFunc(func([]byte) ([]int16, error) { return []int16{1}, nil }), nil
	})
	_, _ = d.decode(f(1))
	fresh := f(1)
	fresh.Voice.SourceEpoch++
	pcm, err := d.decode(fresh)
	if err != nil || created != 2 || pcm.Voice != fresh.Voice {
		t.Fatal("decoder crossed source epoch", err)
	}
	fresh.Voice.ReceiverEpoch++
	fresh.Missing = true
	if _, err := d.decode(fresh); !errors.Is(err, errLossNotConcealed) {
		t.Fatal("PLC reused previous epoch", err)
	}
}

type failFirstEncoder struct{ calls int }

func (e *failFirstEncoder) Encode([]int16) ([]byte, error) {
	e.calls++
	if e.calls == 1 {
		return nil, errors.New("encode failed")
	}
	return []byte{1}, nil
}

func TestEncodingFailureConsumesFrameNumberWithoutEndingSession(t *testing.T) {
	s, _, _ := installedMediaFixture(t)
	_, _, epoch := s.Audio.Snapshot()
	in := make(chan audio.PCMFrame, 2)
	out := make(chan audio.Frame, 2)
	for range 2 {
		in <- audio.PCMFrame{ControlEpoch: epoch, Samples: []int16{1}}
	}
	close(in)
	encoder := &failFirstEncoder{}
	if err := EncodeLoopWithPipeline(context.Background(), encoder, nil, nil, nil, in, out, s); err != nil {
		t.Fatal(err)
	}
	frame, ok := <-out
	if !ok || frame.Sequence != 2 || frame.Voice.SourceEpoch != 1 || frame.CreatedAt.IsZero() {
		t.Fatalf("frame after failure: %+v", frame)
	}
	if _, ok := <-out; ok {
		t.Fatal("failed encoding emitted frame")
	}
}

func TestSendLoopUsesDatagramSequenceAndDropsStaleFrames(t *testing.T) {
	server, conn := newTestConnections(t)
	if err := conn.BindSession(42); err != nil {
		t.Fatal(err)
	}
	s := receiveVoiceFixture()
	s.voice.header.OwnSourceEpoch = 3
	s.voice.header.OwnControlRevision = s.ownControlRevision
	s.voice.header.EnabledDirections = protocol.VoiceDirectionUp
	_, _, epoch := s.Audio.Snapshot()
	identity := audio.VoiceIdentity{Generation: s.Generation(), SourceEpoch: 3}
	in := make(chan audio.Frame, 4)
	in <- audio.Frame{Voice: identity, Sequence: 7, ControlEpoch: epoch, Data: []byte{7}, CreatedAt: time.Now().Add(-time.Second)}
	for _, seq := range []uint32{8, 10} {
		in <- audio.Frame{Voice: identity, Sequence: seq, ControlEpoch: epoch, Data: []byte{byte(seq)}}
	}
	close(in)
	if err := SendLoopWithStats(context.Background(), conn, 42, in, s.Audio, s); err != nil {
		t.Fatal(err)
	}
	server.SetReadDeadline(time.Now().Add(time.Second))
	for i := uint32(1); i <= 2; i++ {
		packet, _, err := server.ReadPacket()
		if err != nil {
			t.Fatal(err)
		}
		bundle, err := protocol.DecodeVoiceBundleV2Packet(packet, true)
		if err != nil || bundle.DatagramSequence != i || bundle.SourceEpoch != 3 || len(bundle.Frames) != int(i) {
			t.Fatalf("sent bundle %+v, %v", bundle, err)
		}
	}
}

func TestVoiceDatagramBaselineSurvivesAudioTransitions(t *testing.T) {
	s := liveTestState()
	generation := s.Generation()
	s.RecordVoiceDatagram(generation, 1)
	s.SetChannelID(2)
	s.RecordVoiceDatagram(generation, 3)
	s.measurements.mu.Lock()
	stream := s.measurements.senders[0]
	missing := len(stream.missing)
	s.measurements.mu.Unlock()
	if missing != 1 {
		t.Fatal("channel transition reset transport loss baseline")
	}
	s.InvalidateSession(ConnectionReconnecting)
	s.RecordVoiceDatagram(generation, 4)
	s.measurements.mu.Lock()
	defer s.measurements.mu.Unlock()
	if s.measurements.datagramMode || len(s.measurements.senders) != 0 {
		t.Fatal("session reset retained transport counters")
	}
}

type failVoiceWriteCodec struct {
	inner    protocol.DatagramCodec
	attempts []uint32
}

func (c *failVoiceWriteCodec) Encode(ctx protocol.DatagramContext, p protocol.VoicePacket) ([]byte, error) {
	b, err := protocol.DecodeVoiceBundleV2Packet(p, true)
	if err != nil {
		return nil, err
	}
	c.attempts = append(c.attempts, b.DatagramSequence)
	if len(c.attempts) == 1 {
		return nil, errors.New("simulated local write failure")
	}
	return c.inner.Encode(ctx, p)
}
func (c *failVoiceWriteCodec) Decode(ctx protocol.DatagramContext, data []byte) (protocol.VoicePacket, error) {
	return c.inner.Decode(ctx, data)
}

func TestSendFailureConsumesDatagramNumberAndContinues(t *testing.T) {
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
	server, err := udp.NewServerPacketConn(serverRaw, serverCodec)
	if err != nil {
		t.Fatal(err)
	}
	codec := &failVoiceWriteCodec{inner: clientCodec}
	conn, err := udp.NewClientPacketConn(clientRaw, codec)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.BindSession(42); err != nil {
		t.Fatal(err)
	}
	in := make(chan audio.Frame, 2)
	for seq := uint32(1); seq <= 2; seq++ {
		in <- audio.Frame{Voice: audio.VoiceIdentity{SourceEpoch: 1}, Sequence: seq, Data: []byte{1}}
	}
	close(in)
	if err := SendLoopWithStats(context.Background(), conn, 42, in, nil, nil); err != nil {
		t.Fatal(err)
	}
	server.SetReadDeadline(time.Now().Add(time.Second))
	packet, _, err := server.ReadPacket()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := protocol.DecodeVoiceBundleV2Packet(packet, true)
	if err != nil || bundle.DatagramSequence != 2 || len(codec.attempts) != 2 || codec.attempts[0] != 1 || codec.attempts[1] != 2 {
		t.Fatalf("write attempts: %+v bundle=%+v, %v", codec.attempts, bundle, err)
	}
}

func TestOwnSourceEpochRestartsEncoderButPeerAndReceiverDoNot(t *testing.T) {
	s, p := voiceClientFixture()
	calls := 0
	configure := func(domain.AudioProfile) error { calls++; return nil }
	if err := s.installVoiceState(s.Generation(), p, configure); err != nil {
		t.Fatal(err)
	}
	p.Header.Revision++
	p.Sources[1].SourceEpoch++
	if err := s.installVoiceState(s.Generation(), p, configure); err != nil {
		t.Fatal(err)
	}
	p.Header.Revision++
	p.Header.OwnControlRevision++
	p.Header.OwnReceiverEpoch++
	if err := s.installVoiceState(s.Generation(), p, configure); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("peer or receiver epoch restarted own encoder")
	}
	p.Header.Revision++
	p.Header.OwnControlRevision++
	p.Header.OwnSourceEpoch++
	p.Sources[0].SourceEpoch++
	if err := s.installVoiceState(s.Generation(), p, configure); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatal("new own source kept the previous encoder stream")
	}
}

func TestSummariesAdvanceAudioHorizonDuringCompleteVoiceOutage(t *testing.T) {
	s, _, _ := installedMediaFixture(t)
	for i, last := range []uint32{0x70000000, 0x90000000} {
		summary := protocol.VoiceSummary{Direction: protocol.VoiceDownstream, Mode: protocol.VoiceMode{DesiredFrames: 2, AllowedFrames: 2}, Intervals: []protocol.VoiceSendInterval{{ID: uint32(i + 1), FirstSequence: last, LastSequence: last, SuccessfulSends: 1}}}
		payload, err := protocol.EncodeVoiceSummary(summary)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AcceptVoiceTelemetry(s.Generation(), protocol.VoicePacket{Type: protocol.PacketVoiceSummary, SessionID: s.SessionID(), Payload: payload}, time.Unix(100+int64(i), 0)); err != nil {
			t.Fatal(err)
		}
	}
	bundle := protocol.VoiceBundleV2{DatagramSequence: 0x90000001, SourceEpoch: 1, ReceiverEpoch: 1}
	if !s.VoiceBundleAllowed(s.Generation(), 8, bundle) {
		t.Fatal("voice remained behind the signed-sequence horizon after recovery")
	}
}
