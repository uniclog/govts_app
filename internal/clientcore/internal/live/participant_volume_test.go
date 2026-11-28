package client

import (
	"math"
	"testing"
)

func TestScalePCMParticipantVolume(t *testing.T) {
	samples := []int16{-1000, 1000, 20000, -20000}
	if got := scalePCM(samples, 0.5); !equalSamples(got, []int16{-500, 500, 10000, -10000}) {
		t.Fatalf("half volume = %v", got)
	}
	if got := scalePCM(samples, 0); !equalSamples(got, []int16{0, 0, 0, 0}) {
		t.Fatalf("muted = %v", got)
	}
	if got := scalePCM(samples, 2); !equalSamples(got, []int16{-2000, 2000, math.MaxInt16, math.MinInt16}) {
		t.Fatalf("amplified volume = %v", got)
	}
	if got := scalePCM(samples, 1); &got[0] != &samples[0] {
		t.Fatal("unity gain copied the sample buffer")
	}
}

func TestParticipantVolumeValidationAndCleanup(t *testing.T) {
	audio := NewAudioControlState(nil)
	if err := audio.SetParticipantVolume(7, 1.5); err != nil {
		t.Fatal(err)
	}
	if got := audio.ParticipantVolume(7); got != 1.5 {
		t.Fatalf("volume = %v", got)
	}
	for _, value := range []float32{-0.1, MaxParticipantVolume + 0.1, float32(math.NaN()), float32(math.Inf(1))} {
		if err := audio.SetParticipantVolume(7, value); err == nil {
			t.Fatalf("accepted invalid volume %v", value)
		}
	}
	audio.RemoveParticipantVolume(7)
	if got := audio.ParticipantVolume(7); got != 1 {
		t.Fatalf("volume after cleanup = %v", got)
	}
}

func equalSamples(a, b []int16) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestNotificationFramesArePlaybackSizedAndBounded(t *testing.T) {
	frames := notificationFrames(NotificationSound{Kind: NotificationJoined, PlaybackEpoch: 4})
	if len(frames) != 6 {
		t.Fatalf("frame count = %d", len(frames))
	}
	for _, frame := range frames {
		if len(frame.Samples) != 960 || frame.PlaybackEpoch != 4 {
			t.Fatalf("invalid notification frame: samples=%d epoch=%d", len(frame.Samples), frame.PlaybackEpoch)
		}
	}
}
