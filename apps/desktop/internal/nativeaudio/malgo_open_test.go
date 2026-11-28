package nativeaudio

import (
	"errors"
	"fmt"
	"testing"

	"github.com/gen2brain/malgo"
)

func TestShouldTryExclusiveCapture(t *testing.T) {
	invalid := errors.Join(
		fmt.Errorf("initialize capture device: %w", malgo.ErrInvalidArgs),
		fmt.Errorf("initialize capture device: %w", malgo.ErrInvalidArgs),
	)
	busy := fmt.Errorf("initialize capture device: %w", malgo.ErrBusy)
	tests := []struct {
		name string
		goos string
		kind malgo.DeviceType
		err  error
		want bool
	}{
		{"windows capture invalid args", "windows", malgo.Capture, invalid, true},
		{"one of joined errors invalid", "windows", malgo.Capture, errors.Join(busy, invalid), true},
		{"busy device", "windows", malgo.Capture, busy, false},
		{"playback", "windows", malgo.Playback, invalid, false},
		{"not windows", "linux", malgo.Capture, invalid, false},
		{"no error", "windows", malgo.Capture, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldTryExclusiveCapture(tt.goos, tt.kind, tt.err); got != tt.want {
				t.Fatalf("shouldTryExclusiveCapture = %t, want %t", got, tt.want)
			}
		})
	}
}
