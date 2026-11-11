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
// The buffer is adaptive: it starts at ExclusiveCaptureBufferStartMS and the
// recorder grows it up to ExclusiveCaptureBufferMaxMS from the measured wake-up
// lateness of the capture thread. It never shrinks: each change reopens the
// exclusive stream, which is a short gap in the voice, and a busy CPU tends to
// stay busy.
const (
	ExclusiveCaptureBufferStepMS  = 20
	ExclusiveCaptureBufferStartMS = 100
	ExclusiveCaptureBufferMaxMS   = 200

	// A buffer of B ms tolerates a wake-up up to B ms late. The controller
	// keeps half of it as reserve: lateness above B/2 grows the buffer.
	exclusiveBufferReserveFactor = 2
)

type exclusiveBufferState struct {
	mu sync.Mutex
	ms int
}

var exclusiveBuffer = exclusiveBufferState{ms: ExclusiveCaptureBufferStartMS}

// ExclusiveCaptureBufferMS is the buffer the next exclusive capture opens with.
func ExclusiveCaptureBufferMS() int {
	exclusiveBuffer.mu.Lock()
	defer exclusiveBuffer.mu.Unlock()
	return exclusiveBuffer.ms
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
	// A stale recorder (the buffer grew meanwhile) only reopens.
	if state.ms != openedMS {
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
