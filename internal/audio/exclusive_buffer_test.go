package audio

import (
	"testing"
	"time"
)

const ms = time.Millisecond

// setBufferState replaces the global buffer state for one test.
func setBufferState(t *testing.T, bufferMS int) {
	t.Helper()
	exclusiveBuffer.mu.Lock()
	savedMS := exclusiveBuffer.ms
	exclusiveBuffer.ms = bufferMS
	exclusiveBuffer.mu.Unlock()
	t.Cleanup(func() {
		exclusiveBuffer.mu.Lock()
		exclusiveBuffer.ms = savedMS
		exclusiveBuffer.mu.Unlock()
	})
}

func TestExclusiveBufferDefaultsToStart(t *testing.T) {
	if ExclusiveCaptureBufferMS() != ExclusiveCaptureBufferStartMS {
		t.Fatalf("default = %d ms, want %d ms", ExclusiveCaptureBufferMS(), ExclusiveCaptureBufferStartMS)
	}
}

func TestAdaptExclusiveBufferGrowth(t *testing.T) {
	tests := []struct {
		name     string
		bufferMS int
		lateness time.Duration
		lost     bool
		want     int
		changed  bool
	}{
		{"lateness within half the buffer", 100, 50 * ms, false, 100, false},
		{"twice the lateness", 100, 70 * ms, false, 140, true},
		{"rounded up to a step", 100, 51 * ms, false, 120, true},
		{"lost audio grows one step", 100, 0, true, 120, true},
		{"lost audio with larger lateness", 100, 90 * ms, true, 180, true},
		{"capped at the maximum", 100, 500 * ms, false, ExclusiveCaptureBufferMaxMS, true},
		{"no growth past the maximum", ExclusiveCaptureBufferMaxMS, 0, true, ExclusiveCaptureBufferMaxMS, false},
		// The buffer never shrinks, however calm the capture thread is.
		{"no shrink without lateness", 160, 0, false, 160, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setBufferState(t, tt.bufferMS)
			got, changed := adaptExclusiveCaptureBuffer(tt.bufferMS, tt.lateness, tt.lost)
			if changed != tt.changed || ExclusiveCaptureBufferMS() != tt.want {
				t.Fatalf("adapt = %d, %t; buffer %d ms; want %d ms, changed %t", got, changed, ExclusiveCaptureBufferMS(), tt.want, tt.changed)
			}
			if changed && got != tt.want {
				t.Fatalf("returned %d ms, want %d", got, tt.want)
			}
		})
	}
}

func TestAdaptExclusiveBufferNeverShrinksOverManyWindows(t *testing.T) {
	setBufferState(t, 100)
	adaptExclusiveCaptureBuffer(100, 80*ms, false)
	// An hour of 5 s windows without lateness.
	for i := 0; i < 720; i++ {
		if _, changed := adaptExclusiveCaptureBuffer(160, 0, false); changed {
			t.Fatalf("window %d changed the buffer to %d ms", i, ExclusiveCaptureBufferMS())
		}
	}
	if ExclusiveCaptureBufferMS() != 160 {
		t.Fatalf("buffer = %d ms, want 160", ExclusiveCaptureBufferMS())
	}
}

func TestAdaptExclusiveBufferIgnoresStaleRecorder(t *testing.T) {
	setBufferState(t, 140)
	if _, changed := adaptExclusiveCaptureBuffer(100, 200*ms, true); changed || ExclusiveCaptureBufferMS() != 140 {
		t.Fatalf("a recorder opened with another buffer changed it to %d ms", ExclusiveCaptureBufferMS())
	}
}

func TestRoundUpBufferStep(t *testing.T) {
	for _, tt := range []struct {
		in   time.Duration
		want int
	}{{0, 0}, {1, 20}, {20 * ms, 20}, {21 * ms, 40}, {139 * ms, 140}} {
		if got := roundUpBufferStep(tt.in); got != tt.want {
			t.Fatalf("roundUpBufferStep(%s) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
