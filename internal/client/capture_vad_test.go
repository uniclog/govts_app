package client

import (
	"context"
	"errors"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/audio"
	"uniclog.io/sonoryx/internal/audio/vad"
	"uniclog.io/sonoryx/internal/audio/voicegate"
)

type countingEncoder struct{ calls int }

func TestNormalizedMeterLevel(t *testing.T) {
	tests := []struct {
		level float32
		want  float32
	}{
		{level: -96, want: 0},
		{level: -60, want: 0},
		{level: -30, want: 0.5},
		{level: 0, want: 1},
	}
	for _, test := range tests {
		if got := normalizedMeterLevel(test.level); got != test.want {
			t.Errorf("normalizedMeterLevel(%v) = %v, want %v", test.level, got, test.want)
		}
	}
}

func (e *countingEncoder) Encode(samples []int16) ([]byte, error) {
	e.calls++
	return []byte{byte(samples[0])}, nil
}

type sequenceDetector struct {
	results []vad.Result
	calls   int
	err     error
}

func (d *sequenceDetector) Analyze([]int16) (vad.Result, error) {
	d.calls++
	if d.err != nil {
		return vad.Result{}, d.err
	}
	result := d.results[0]
	d.results = d.results[1:]
	return result, nil
}
func (*sequenceDetector) Reset() error { return nil }
func (*sequenceDetector) Close() error { return nil }

type blockingFilter struct {
	calls   int
	entered chan struct{}
	release chan struct{}
}

func (f *blockingFilter) Process([]int16) error {
	f.calls++
	if f.calls == 3 {
		close(f.entered)
		<-f.release
	}
	return nil
}
func (*blockingFilter) Reset() error { return nil }
func (*blockingFilter) Close() error { return nil }

func runCapturePipeline(t *testing.T, state *State, detector vad.Detector, samples ...int16) ([]audio.Frame, int, error) {
	t.Helper()
	encoder := &countingEncoder{}
	gate, err := voicegate.New(state.Audio.VADSnapshot().GateConfig())
	if err != nil {
		t.Fatal(err)
	}
	pcmCh := make(chan audio.PCMFrame, len(samples))
	_, _, epoch := state.Audio.Snapshot()
	for _, sample := range samples {
		pcmCh <- audio.PCMFrame{Samples: []int16{sample}, Duration: 20 * time.Millisecond, ControlEpoch: epoch}
	}
	close(pcmCh)
	audioCh := make(chan audio.Frame, len(samples)+3)
	err = EncodeLoopWithPipeline(context.Background(), encoder, nil, detector, gate, pcmCh, audioCh, state)
	var frames []audio.Frame
	for frame := range audioCh {
		frames = append(frames, frame)
	}
	return frames, encoder.calls, err
}

func TestCaptureVoiceGateSkipsEncodeUntilSpeechConfirmed(t *testing.T) {
	state := liveTestState()
	if err := state.Audio.SetVADMode(voicegate.ModeVAD); err != nil {
		t.Fatal(err)
	}
	state.Audio.SetVADEnabled(true)
	detector := &sequenceDetector{results: []vad.Result{
		{}, {}, {Speech: true}, {Speech: true}, {Speech: true},
	}}
	frames, calls, err := runCapturePipeline(t, state, detector, 0, 0, 1, 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || len(frames) != 3 {
		t.Fatalf("encoded=%d frames=%d, want 3", calls, len(frames))
	}
	if len(frames) > maxMixerFramesPerSender {
		t.Fatalf("onset burst=%d exceeds receiver mixer capacity=%d", len(frames), maxMixerFramesPerSender)
	}
	for i, frame := range frames {
		if len(frame.Data) != 1 || frame.Data[0] != byte(i+1) {
			t.Fatalf("frame %d = %+v", i, frame)
		}
	}
	if state.Audio.VADSnapshot().Open {
		t.Fatal("closed pipeline left VAD open")
	}
}

func TestCaptureDisabledVADIsPassthrough(t *testing.T) {
	state := liveTestState()
	detector := &sequenceDetector{err: errors.New("must not be called")}
	frames, calls, err := runCapturePipeline(t, state, detector, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(frames) != 2 || detector.calls != 0 {
		t.Fatalf("calls=%d frames=%d detector=%d", calls, len(frames), detector.calls)
	}
}

func TestCaptureVADFailureIsClosedAndReturned(t *testing.T) {
	state := liveTestState()
	if err := state.Audio.SetVADMode(voicegate.ModeVAD); err != nil {
		t.Fatal(err)
	}
	state.Audio.SetVADEnabled(true)
	want := errors.New("VAD failed")
	detector := &sequenceDetector{err: want}
	frames, calls, err := runCapturePipeline(t, state, detector, 1)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if calls != 0 || len(frames) != 0 || state.Audio.VADSnapshot().Open {
		t.Fatal("failed VAD leaked audio or open state")
	}
}

func TestCaptureMuteBarrierRejectsBufferedPreRoll(t *testing.T) {
	state := liveTestState()
	if err := state.Audio.SetVADMode(voicegate.ModeVAD); err != nil {
		t.Fatal(err)
	}
	state.Audio.SetVADEnabled(true)
	filter := &blockingFilter{entered: make(chan struct{}), release: make(chan struct{})}
	detector := &sequenceDetector{results: []vad.Result{{Speech: true}, {Speech: true}, {Speech: true}}}
	encoder := &countingEncoder{}
	gate, err := voicegate.New(state.Audio.VADSnapshot().GateConfig())
	if err != nil {
		t.Fatal(err)
	}
	pcmCh := make(chan audio.PCMFrame, 3)
	_, _, oldEpoch := state.Audio.Snapshot()
	for i := int16(1); i <= 3; i++ {
		pcmCh <- audio.PCMFrame{Samples: []int16{i}, Duration: 20 * time.Millisecond, ControlEpoch: oldEpoch}
	}
	close(pcmCh)
	audioCh := make(chan audio.Frame, 3)
	done := make(chan error, 1)
	go func() {
		done <- EncodeLoopWithPipeline(context.Background(), encoder, filter, detector, gate, pcmCh, audioCh, state)
	}()
	<-filter.entered
	state.Audio.SetMuted(true)
	state.Audio.SetMuted(false)
	close(filter.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if encoder.calls != 0 || len(audioCh) != 0 {
		t.Fatalf("stale pre-roll escaped mute barrier: encodes=%d frames=%d", encoder.calls, len(audioCh))
	}
}
