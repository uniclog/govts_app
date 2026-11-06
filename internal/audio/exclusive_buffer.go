package audio

import (
	"log"
	"sync"
	"time"
)

// The WASAPI buffer of the exclusive capture fallback. It is also how late the
// capture thread may wake before the device overwrites unread audio, so a
// larger buffer survives a busier CPU at the cost of the same amount of
// latency. Shared-mode capture ignores it.
//
// By default the buffer is adaptive: it starts at ExclusiveCaptureBufferStartMS
// and the recorder grows it up to ExclusiveCaptureBufferMaxMS from the measured
// wake-up lateness of the capture thread. It never shrinks: each change
// reopens the exclusive stream, which is a short gap in the voice, and a busy
// CPU tends to stay busy. With adaptation off it is
// ExclusiveCaptureBufferFixedMS.
const (
	ExclusiveCaptureBufferStepMS  = 20
	ExclusiveCaptureBufferStartMS = 100
	ExclusiveCaptureBufferMaxMS   = 200
	ExclusiveCaptureBufferFixedMS = 180

	// A buffer of B ms tolerates a wake-up up to B ms late. The controller
	// keeps half of it as reserve: lateness above B/2 grows the buffer.
	exclusiveBufferReserveFactor = 2
)

type exclusiveBufferState struct {
	mu       sync.Mutex
	adaptive bool
	ms       int
}

var exclusiveBuffer = exclusiveBufferState{adaptive: true, ms: ExclusiveCaptureBufferStartMS}

// ExclusiveCaptureBufferMS is the buffer the next exclusive capture opens with.
func ExclusiveCaptureBufferMS() int {
	exclusiveBuffer.mu.Lock()
	defer exclusiveBuffer.mu.Unlock()
	return exclusiveBuffer.ms
}

// ExclusiveCaptureBufferAdaptive reports whether the recorder picks the buffer.
func ExclusiveCaptureBufferAdaptive() bool {
	exclusiveBuffer.mu.Lock()
	defer exclusiveBuffer.mu.Unlock()
	return exclusiveBuffer.adaptive
}

// SetExclusiveCaptureBufferAdaptive switches between the adaptive buffer,
// which restarts from ExclusiveCaptureBufferStartMS, and the fixed one. An open
// exclusive recorder reopens with the new value on its next read.
func SetExclusiveCaptureBufferAdaptive(adaptive bool) {
	exclusiveBuffer.mu.Lock()
	defer exclusiveBuffer.mu.Unlock()
	if exclusiveBuffer.adaptive == adaptive {
		return
	}
	exclusiveBuffer.adaptive = adaptive
	exclusiveBuffer.ms = ExclusiveCaptureBufferFixedMS
	if adaptive {
		exclusiveBuffer.ms = ExclusiveCaptureBufferStartMS
	}
	log.Printf("exclusive capture buffer mode: adaptive=%t buffer_ms=%d", adaptive, exclusiveBuffer.ms)
}

// adaptExclusiveCaptureBuffer is called once per capture measurement window by
// the exclusive recorder that opened with openedMS. lateness is the largest
// wake-up delay of the capture thread in the window; lost reports audio the
// device delivered short of real time or chunks the queue dropped. It returns
// the new buffer when it grew; the recorder then reopens.
func adaptExclusiveCaptureBuffer(openedMS int, lateness time.Duration, lost bool) (int, bool) {
	exclusiveBuffer.mu.Lock()
	defer exclusiveBuffer.mu.Unlock()
	state := &exclusiveBuffer
	// A stale recorder (mode or value changed meanwhile) only reopens.
	if !state.adaptive || state.ms != openedMS {
		return 0, false
	}
	needed := roundUpBufferStep(time.Duration(exclusiveBufferReserveFactor) * lateness)
	if lost {
		needed = max(needed, state.ms+ExclusiveCaptureBufferStepMS)
	}
	needed = min(needed, ExclusiveCaptureBufferMaxMS)
	if needed <= state.ms {
		return 0, false
	}
	previous := state.ms
	state.ms = needed
	log.Printf("exclusive capture buffer grown: from_ms=%d to_ms=%d lateness=%s lost=%t", previous, needed, lateness.Round(time.Millisecond), lost)
	return needed, true
}

func roundUpBufferStep(d time.Duration) int {
	step := time.Duration(ExclusiveCaptureBufferStepMS) * time.Millisecond
	return int((d+step-1)/step) * ExclusiveCaptureBufferStepMS
}
