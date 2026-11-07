package audio

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"
)

// Stream sound is captured as 48 kHz stereo signed 16-bit little-endian PCM,
// the rate WebRTC Opus encodes, so the WebView needs no resampling.
const (
	LoopbackSampleRate = 48000
	LoopbackChannels   = 2
	loopbackFrameBytes = LoopbackChannels * 2
	// A reader that falls behind loses the oldest sound, not latency.
	loopbackMaxBuffered = LoopbackSampleRate * loopbackFrameBytes / 4  // 250 ms
	loopbackMinRead     = LoopbackSampleRate * loopbackFrameBytes / 50 // 20 ms
)

// ErrLoopbackUnsupported means this system cannot capture per process
// (Windows before process loopback, macOS before 14.2, or another OS).
var ErrLoopbackUnsupported = errors.New("process loopback capture is unavailable")

var errLoopbackClosed = errors.New("loopback capture stopped")

// LoopbackTarget selects the captured sound: the render streams of
// ProcessID and its child processes, or, with Exclude, everything else.
type LoopbackTarget struct {
	ProcessID uint32
	Exclude   bool
}

// LoopbackApp is a process with a visible top-level window.
type LoopbackApp struct {
	ProcessID uint32
	Name      string
	Title     string
}

type LoopbackCapture struct {
	mu      sync.Mutex
	pending []byte
	dropped uint64
	err     error
	wake    chan struct{}
	done    chan struct{}
	once    sync.Once
	stop    func()
}

func newLoopbackCapture() *LoopbackCapture {
	return &LoopbackCapture{wake: make(chan struct{}, 1), done: make(chan struct{})}
}

// push is called from the capture thread with PCM that it still owns.
func (c *LoopbackCapture) push(pcm []byte) {
	c.mu.Lock()
	c.pending = append(c.pending, pcm...)
	if excess := len(c.pending) - loopbackMaxBuffered; excess > 0 {
		excess += (loopbackFrameBytes - excess%loopbackFrameBytes) % loopbackFrameBytes
		c.dropped += uint64(excess)
		c.pending = append(c.pending[:0], c.pending[excess:]...)
	}
	ready := len(c.pending) >= loopbackMinRead
	c.mu.Unlock()
	if ready {
		select {
		case c.wake <- struct{}{}:
		default:
		}
	}
}

// finish ends the capture; err is nil for a requested stop.
func (c *LoopbackCapture) finish(err error) {
	c.once.Do(func() {
		c.mu.Lock()
		c.err = err
		c.mu.Unlock()
		close(c.done)
	})
}

// Read waits up to wait for at least 20 ms of sound and returns everything
// pending, possibly nothing while the target is silent.
func (c *LoopbackCapture) Read(ctx context.Context, wait time.Duration) ([]byte, error) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		c.mu.Lock()
		if len(c.pending) >= loopbackMinRead {
			out := c.pending
			c.pending = make([]byte, 0, cap(out))
			c.mu.Unlock()
			return out, nil
		}
		c.mu.Unlock()
		select {
		case <-c.wake:
		case <-c.done:
			c.mu.Lock()
			err := c.err
			c.mu.Unlock()
			if err == nil {
				err = errLoopbackClosed
			}
			return nil, err
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			c.mu.Lock()
			out := c.pending
			c.pending = make([]byte, 0, cap(out))
			c.mu.Unlock()
			return out, nil
		}
	}
}

// Dropped reports PCM bytes discarded because the reader fell behind.
func (c *LoopbackCapture) Dropped() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped
}

// Close stops the capture and waits for its thread to release the device.
func (c *LoopbackCapture) Close() {
	if c.stop != nil {
		c.stop()
	}
	<-c.done
}

var (
	loopbackProbeMu sync.Mutex
	loopbackWorks   bool
	// loopbackAvailable, when set, answers LoopbackSupported without a test
	// capture. On macOS a test tap would raise the system audio permission
	// prompt before the user starts a stream.
	loopbackAvailable func() bool
)

// LoopbackSupported probes whether per-process capture can start. Only
// success is remembered: a failure may be transient (audio service restart),
// and a failed activation costs milliseconds.
func LoopbackSupported() bool {
	if loopbackAvailable != nil {
		return loopbackAvailable()
	}
	loopbackProbeMu.Lock()
	defer loopbackProbeMu.Unlock()
	if loopbackWorks {
		return true
	}
	capture, err := StartLoopback(LoopbackTarget{ProcessID: currentProcessID(), Exclude: true})
	if err != nil {
		return false
	}
	capture.Close()
	loopbackWorks = true
	return true
}

// CurrentProcessID is the target that excludes Sonoryx itself: the voice
// chat player and its WebView2 child processes.
func CurrentProcessID() uint32 { return currentProcessID() }

func currentProcessID() uint32 { return uint32(os.Getpid()) }
