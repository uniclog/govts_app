package client

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"uniclog.io/govts/internal/audio"
	"uniclog.io/govts/internal/audio/voicegate"
	"uniclog.io/govts/internal/protocol"
)

func TestSpeakingDetectorBoundariesAndHangover(t *testing.T) {
	now := time.Now()
	d := SpeakingDetector{}
	for _, pcm := range [][]int16{nil, {}, {0}, {599, -599}} {
		if d.Observe(pcm, now) {
			t.Fatal("silence/below threshold activated")
		}
	}
	if !d.Observe([]int16{600, -600}, now) {
		t.Fatal("threshold not inclusive")
	}
	if !d.Observe([]int16{0}, now.Add(299*time.Millisecond)) {
		t.Fatal("hangover cut short")
	}
	if d.Speaking(now.Add(300 * time.Millisecond)) {
		t.Fatal("hangover did not end")
	}
	if !d.Observe([]int16{-32768}, now) {
		t.Fatal("PCM square overflow")
	}
}

func TestAudioMeterClampsAndAdvancesSequence(t *testing.T) {
	controls := NewAudioControlState(nil)
	controls.SetAudioMeter(-1, 0.4, 2)
	first := controls.AudioMeterSnapshot()
	if first.Input != 0 || first.Processed != 0.4 || first.Transmitted != 1 || first.Sequence != 1 {
		t.Fatalf("first meter sample = %+v", first)
	}
	controls.SetAudioMeter(0.25, 0.35, 0.5)
	second := controls.AudioMeterSnapshot()
	if second.Input != 0.25 || second.Processed != 0.35 || second.Transmitted != 0.5 || second.Sequence != 2 {
		t.Fatalf("second meter sample = %+v", second)
	}
}

func TestRNNoiseSensitivityValidation(t *testing.T) {
	controls := NewAudioControlState(nil)
	if controls.RNNoiseSensitivity() != 1 {
		t.Fatalf("default RNNoise sensitivity = %v", controls.RNNoiseSensitivity())
	}
	if err := controls.SetRNNoiseSensitivity(0.65); err != nil {
		t.Fatal(err)
	}
	if err := controls.SetRNNoiseSensitivity(1.1); err == nil {
		t.Fatal("invalid RNNoise sensitivity was accepted")
	}
	if controls.RNNoiseSensitivity() != 0.65 {
		t.Fatalf("RNNoise sensitivity = %v", controls.RNNoiseSensitivity())
	}
}

func TestVADResultControlsLocalSpeaking(t *testing.T) {
	s := liveTestState()
	now := time.Now()
	id := s.SessionID()

	s.ObserveVoiceActivity(id, false, now)
	if s.SnapshotView().Speaking[id] {
		t.Fatal("inactive VAD result activated speaking")
	}
	s.ObserveVoiceActivity(id, true, now)
	if !s.SnapshotView().Speaking[id] {
		t.Fatal("active VAD result did not activate speaking")
	}
}

func TestMuteWaitsForSendAndRejectsOldEpochAfterUnmute(t *testing.T) {
	a := NewAudioControlState(nil)
	_, _, epoch := a.Snapshot()
	entered, release := make(chan struct{}), make(chan struct{})
	sent := make(chan struct{})
	go func() { a.Send(epoch, func() error { close(entered); <-release; return nil }); close(sent) }()
	<-entered
	muted := make(chan struct{})
	go func() { a.SetMuted(true); close(muted) }()
	select {
	case <-muted:
		t.Fatal("mute acknowledged before in-flight send completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-sent
	<-muted
	called := false
	a.Send(epoch, func() error { called = true; return nil })
	a.SetMuted(false)
	a.Send(epoch, func() error { called = true; return nil })
	if called {
		t.Fatal("stale frame escaped mute barrier")
	}
	_, _, current := a.Snapshot()
	a.Send(current, func() error { called = true; return nil })
	if !called {
		t.Fatal("unmute failed")
	}
}

type controlPlayer struct {
	mu             sync.Mutex
	deafened       bool
	writes, clears int
}

func (p *controlPlayer) Write([]int16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.deafened {
		p.writes++
	}
	return nil
}
func (p *controlPlayer) Close() error { return nil }
func (p *controlPlayer) SetDeafened(v bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.deafened = v
	if v {
		p.clears++
	}
	return nil
}

func TestAudioCommandsPersistAcrossReconnectAndDiscardOldPlayback(t *testing.T) {
	s := liveTestState()
	var output bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	HandleOfflineCommand(Command{Name: "/mute", Arguments: []string{"on"}}, &output, cancel, s)
	HandleOfflineCommand(Command{Name: "/deafen", Arguments: []string{"on"}}, &output, cancel, s)
	HandleOfflineCommand(Command{Name: "/rnnoise"}, &output, cancel, s)
	HandleOfflineCommand(Command{Name: "/vad", Arguments: []string{"on"}}, &output, cancel, s)
	HandleOfflineCommand(Command{Name: "/vad-mode", Arguments: []string{"vad"}}, &output, cancel, s)
	HandleOfflineCommand(Command{Name: "/vad-sensitivity", Arguments: []string{"0.7"}}, &output, cancel, s)
	s.InvalidateSession(ConnectionReconnecting)
	s.StartSession(99)
	v := s.SnapshotView()
	if !v.Muted || !v.Deafened || v.RNNoiseEnabled || !v.VADEnabled || v.VADMode != "vad" || v.VADSensitivity != 0.7 || ctx.Err() != nil {
		t.Fatal("offline flags lost")
	}
	p := &controlPlayer{}
	detach, err := s.Audio.AttachPlayer(p)
	if err != nil {
		t.Fatal(err)
	}
	defer detach()
	if !p.deafened || p.clears != 1 {
		t.Fatal("new player did not inherit deafen")
	}
	_, old := s.Audio.PlaybackSnapshot()
	s.Audio.SetDeafened(false)
	s.Audio.Play(old, func() error { return p.Write(nil) })
	_, current := s.Audio.PlaybackSnapshot()
	s.Audio.Play(current, func() error { return p.Write(nil) })
	if p.writes != 1 {
		t.Fatalf("writes=%d", p.writes)
	}
	HandleAudioCommand(Command{Name: "/mute", Arguments: []string{"off"}}, s, &output)
	HandleAudioCommand(Command{Name: "/mute", Arguments: []string{"off"}}, s, &output)
	if muted, _, _ := s.Audio.Snapshot(); muted {
		t.Fatal("off is not idempotent")
	}
}

func TestVADCommandsValidateSettings(t *testing.T) {
	s := liveTestState()
	var output bytes.Buffer

	if !HandleAudioCommand(Command{Name: "/vad-sensitivity", Arguments: []string{"1.1"}}, s, &output) {
		t.Fatal("VAD sensitivity command was not handled")
	}
	settings := s.Audio.VADSnapshot()
	if settings.Sensitivity != voicegate.DefaultSensitivity {
		t.Fatalf("invalid sensitivity changed state to %v", settings.Sensitivity)
	}
	if !strings.Contains(output.String(), "between 0 and 1") {
		t.Fatalf("missing validation message: %q", output.String())
	}
	output.Reset()
	HandleAudioCommand(Command{Name: "/vad-mode", Arguments: []string{"unknown"}}, s, &output)
	if s.Audio.VADSnapshot().Mode != voicegate.ModeHybrid || !strings.Contains(output.String(), "level, vad, or hybrid") {
		t.Fatalf("invalid mode changed state: %q", output.String())
	}
}

func TestVADOpenIsTransientObservableAndMuteClosesIt(t *testing.T) {
	s := liveTestState()
	s.Audio.SetVADEnabled(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changed, unsubscribe := s.Subscribe(ctx)
	defer unsubscribe()
	<-changed

	s.Audio.SetVADOpen(true)
	if !s.SnapshotView().VADOpen {
		t.Fatal("open gate missing from view")
	}
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Fatal("open transition did not notify subscriber")
	}
	s.Audio.SetMuted(true)
	view := s.SnapshotView()
	if view.VADOpen || !view.Muted {
		t.Fatalf("mute did not close gate in view: %+v", view)
	}
}

func TestSendLoopRejectsQueuedFramesFromBeforeMute(t *testing.T) {
	peer := newJoinTestPeer(t)
	a := peer.state.Audio
	_, _, old := a.Snapshot()
	a.SetMuted(true)
	a.SetMuted(false)
	_, _, current := a.Snapshot()
	frames := make(chan audio.Frame, 2)
	frames <- audio.Frame{ControlEpoch: old, Data: []byte{1}}
	frames <- audio.Frame{ControlEpoch: current, Data: []byte{2}}
	close(frames)
	if err := SendLoop(peer.ctx, peer.clientConn, 42, frames, a); err != nil {
		t.Fatal(err)
	}
	packet, _, err := peer.receiveRequest()
	if err != nil || packet.Type != protocol.PacketVoice || packet.Sequence != 1 || !bytes.Equal(packet.Payload, []byte{2}) {
		t.Fatalf("sent old frame: %+v, %v", packet, err)
	}
	peer.serverConn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, _, err := peer.serverConn.ReadPacket(); err == nil {
		t.Fatal("extra voice packet")
	}
}

func TestDecodeWhileDeafenedStillUpdatesSpeaking(t *testing.T) {
	s := liveTestState()
	s.Audio.SetDeafened(true)
	input := make(chan audio.MediaFrame, 1)
	input <- audio.MediaFrame{SenderID: 2}
	close(input)
	output := make(chan audio.MediaPCMFrame, 1)
	if err := DecodeLoop(context.Background(), func() (audio.Decoder, error) {
		return decoderFunc(func([]byte) ([]int16, error) { return []int16{1000}, nil }), nil
	}, input, output, s); err != nil {
		t.Fatal(err)
	}
	if _, ok := <-output; ok {
		t.Fatal("deafened PCM reached mixer")
	}
	if !s.SnapshotView().Speaking[2] {
		t.Fatal("deafen disabled speaking detection")
	}
}

func TestUnmuteRejectedWithoutCaptureDevice(t *testing.T) {
	controls := NewAudioControlState(nil)
	controls.SetCaptureAvailable(false)
	if muted, _, _ := controls.Snapshot(); !muted {
		t.Fatal("missing microphone left capture unmuted")
	}
	if err := controls.SetMuted(false); err == nil {
		t.Fatal("unmute allowed without a microphone")
	}
	if muted, _, _ := controls.Snapshot(); !muted {
		t.Fatal("rejected unmute changed mute")
	}
	controls.SetCaptureAvailable(true)
	if muted, _, _ := controls.Snapshot(); !muted {
		t.Fatal("restoring capture unmuted the microphone")
	}
	if err := controls.SetMuted(false); err != nil {
		t.Fatal(err)
	}
	if muted, _, _ := controls.Snapshot(); muted {
		t.Fatal("unmute failed after capture returned")
	}
}
