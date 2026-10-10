package voicegate

import (
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/audio"
	"uniclog.io/sonoryx/internal/audio/rnnoise"
	"uniclog.io/sonoryx/internal/audio/vad"
)

const testFrameDuration = 20 * time.Millisecond

func testFrame(value int16, epoch uint64) audio.PCMFrame {
	return audio.PCMFrame{Samples: []int16{value}, Duration: testFrameDuration, ControlEpoch: epoch}
}

func TestThresholdAndModes(t *testing.T) {
	if ThresholdDBFS(0) != -20 || ThresholdDBFS(1) != -55 {
		t.Fatalf("threshold endpoints = %g, %g", ThresholdDBFS(0), ThresholdDBFS(1))
	}
	for _, tc := range []struct {
		name   string
		mode   Mode
		result vad.Result
		want   bool
	}{
		{"disabled ignores result", ModeDisabled, vad.Result{}, true},
		{"level accepts loud non-speech", ModeLevel, vad.Result{LevelDBFS: -10}, true},
		{"level rejects quiet speech", ModeLevel, vad.Result{Speech: true, LevelDBFS: -60}, false},
		{"vad accepts quiet speech", ModeVAD, vad.Result{Speech: true, LevelDBFS: -60}, true},
		{"vad rejects loud non-speech", ModeVAD, vad.Result{LevelDBFS: -10}, false},
		{"hybrid accepts both", ModeHybrid, vad.Result{Speech: true, LevelDBFS: -10}, true},
		{"hybrid rejects non-speech", ModeHybrid, vad.Result{LevelDBFS: -10}, false},
		{"hybrid rejects quiet speech", ModeHybrid, vad.Result{Speech: true, LevelDBFS: -60}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := DefaultConfig()
			config.Mode = tc.mode
			config.Sensitivity = 0
			if got := Active(config, tc.result); got != tc.want {
				t.Fatalf("Active(%s) = %t, want %t", tc.mode, got, tc.want)
			}
		})
	}
}

func TestGateCandidatePreRollHangoverAndOwnership(t *testing.T) {
	config := DefaultConfig()
	config.Mode = ModeVAD
	gate, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	active := vad.Result{Speech: true}
	for i := int16(1); i <= 2; i++ {
		if frames := gate.Process(testFrame(i, 7), active); len(frames) != 0 {
			t.Fatalf("candidate emitted %d frames", len(frames))
		}
	}
	third := testFrame(3, 7)
	frames := gate.Process(third, active)
	if len(frames) != 3 || frames[0].Samples[0] != 1 || frames[1].Samples[0] != 2 || frames[2].Samples[0] != 3 {
		t.Fatalf("opened frames = %+v", frames)
	}
	third.Samples[0] = 99
	if frames[2].Samples[0] != 3 {
		t.Fatal("pre-roll aliases input samples")
	}
	if !gate.IsOpen() {
		t.Fatal("gate did not open")
	}
	inactive := vad.Result{}
	for i := 0; i < 14; i++ {
		if got := gate.Process(testFrame(0, 7), inactive); len(got) != 1 {
			t.Fatalf("hangover frame %d was not emitted", i)
		}
	}
	if got := gate.Process(testFrame(0, 7), inactive); len(got) != 0 || gate.State() != Closed {
		t.Fatalf("gate did not close at 300ms: state=%v frames=%d", gate.State(), len(got))
	}
}

func TestGateResetsCandidateOnInactiveEpochAndConfig(t *testing.T) {
	config := DefaultConfig()
	config.Mode = ModeVAD
	gate, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	active := vad.Result{Speech: true}
	gate.Process(testFrame(1, 1), active)
	gate.Process(testFrame(0, 1), vad.Result{})
	if gate.State() != Closed {
		t.Fatal("inactive candidate did not close")
	}
	gate.Process(testFrame(1, 1), active)
	gate.Process(testFrame(2, 2), active)
	if gate.State() != Candidate {
		t.Fatal("new epoch reused candidate duration")
	}
	config.Sensitivity = 0.6
	if err := gate.Configure(config); err != nil {
		t.Fatal(err)
	}
	if gate.State() != Closed {
		t.Fatal("config change did not reset gate")
	}
}

func TestGateReopensDuringHangoverAndDisabledBypasses(t *testing.T) {
	config := DefaultConfig()
	config.Mode = ModeVAD
	gate, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	active := vad.Result{Speech: true}
	for i := 0; i < 3; i++ {
		gate.Process(testFrame(int16(i+1), 1), active)
	}
	gate.Process(testFrame(0, 1), vad.Result{})
	if gate.State() != Hangover {
		t.Fatal("inactive open gate did not enter hangover")
	}
	if frames := gate.Process(testFrame(4, 1), active); len(frames) != 1 || gate.State() != Open {
		t.Fatal("active frame did not reopen hangover")
	}
	config.Mode = ModeDisabled
	if err := gate.Configure(config); err != nil {
		t.Fatal(err)
	}
	frame := testFrame(5, 1)
	frames := gate.Process(frame, vad.Result{})
	if len(frames) != 1 || frames[0].Samples[0] != 5 || gate.State() != Closed {
		t.Fatalf("disabled gate did not bypass: %+v state=%v", frames, gate.State())
	}
}

func TestGateValidation(t *testing.T) {
	for _, mutate := range []func(*Config){
		func(c *Config) { c.Mode = "unknown" },
		func(c *Config) { c.Sensitivity = -0.1 },
		func(c *Config) { c.MinSpeech = 0 },
		func(c *Config) { c.PreRoll = 20 * time.Millisecond },
		func(c *Config) { c.PreRoll = MaxPreRoll + time.Millisecond },
		func(c *Config) { c.Hangover = -time.Millisecond },
	} {
		config := DefaultConfig()
		mutate(&config)
		if _, err := New(config); err == nil {
			t.Fatalf("invalid config accepted: %+v", config)
		}
	}
}

func BenchmarkMicrophoneFilterVADGate48k20ms(b *testing.B) {
	filter, err := rnnoise.New()
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = filter.Close() })
	detector, err := vad.NewWebRTC(vad.WebRTCConfig{
		SampleRate:      48000,
		Channels:        1,
		SamplesPerFrame: 960,
		Aggressiveness:  vad.ModeAggressive,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = detector.Close() })
	config := DefaultConfig()
	config.Mode = ModeHybrid
	gate, err := New(config)
	if err != nil {
		b.Fatal(err)
	}
	frame := audio.PCMFrame{Samples: make([]int16, 960), Duration: testFrameDuration, ControlEpoch: 1}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if err := filter.Process(frame.Samples); err != nil {
			b.Fatal(err)
		}
		result, err := detector.Analyze(frame.Samples)
		if err != nil {
			b.Fatal(err)
		}
		gate.Process(frame, result)
	}
}
