package webrtcaudio

import (
	"errors"
	"io"
	"sync"
	"time"

	"uniclog.io/sonoryx/internal/audio"
)

const (
	sampleRate   = 48000
	frameSamples = 960
	frameTime    = 20 * time.Millisecond
	maxQueued    = 3
	maxAge       = maxQueued * frameTime
)

var errFormat = errors.New("WebRTC bridge requires 48 kHz mono PCM in 20 ms frames")

type frame struct {
	samples [frameSamples]int16
	at      time.Time
}
type queue struct {
	frames     [maxQueued]frame
	head, size int
}

func (q *queue) put(f frame) {
	if q.size == maxQueued {
		q.head = (q.head + 1) % maxQueued
		q.size--
	}
	q.frames[(q.head+q.size)%maxQueued] = f
	q.size++
}
func (q *queue) take(now time.Time) (frame, bool) {
	for q.size > 0 {
		f := q.frames[q.head]
		q.size--
		q.head = (q.head + 1) % maxQueued
		if now.Sub(f.at) <= maxAge {
			return f, true
		}
	}
	return frame{}, false
}
func (q *queue) clear() { q.size = 0; q.head = 0 }

type Bridge struct {
	mu                      sync.Mutex
	ready                   *sync.Cond
	capture, playback       queue
	peer                    uint64
	epoch                   uint64
	playbackEpoch           uint64
	recorder                *recorder
	player                  *player
	muted, moving, deafened bool
	sinkMu                  sync.Mutex
}

func NewBridge() *Bridge {
	b := &Bridge{muted: true}
	b.ready = sync.NewCond(&b.mu)
	return b
}
func (b *Bridge) NewSession() audio.Session { return b }

func (b *Bridge) clearLocked() {
	b.capture.clear()
	b.playback.clear()
	b.epoch++
	b.playbackEpoch++
}
func (b *Bridge) Flush() {
	b.sinkMu.Lock()
	defer b.sinkMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.clearLocked()
}
func (b *Bridge) SetPeer(id uint64) {
	b.sinkMu.Lock()
	defer b.sinkMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.peer = id
	b.clearLocked()
}

func (b *Bridge) Mute(value bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.muted = value
	b.capture.clear()
	b.epoch++
}
func (b *Bridge) Move(value bool) {
	b.sinkMu.Lock()
	defer b.sinkMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	b.moving = value
	b.clearLocked()
}
func (b *Bridge) captureEpoch(id uint64) (uint64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.epoch, b.peer == id && b.recorder != nil && !b.muted && !b.moving
}
func (b *Bridge) push(id, epoch uint64, f frame) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.peer == id && b.epoch == epoch && b.recorder != nil && !b.muted && !b.moving {
		b.capture.put(f)
		b.ready.Signal()
	}
}
func (b *Bridge) OpenRecorder(config audio.CodecConfig) (audio.Recorder, error) {
	if config.SampleRate != sampleRate || config.Channels != 1 || config.SamplesPerFrame != frameSamples {
		return nil, errFormat
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	r := &recorder{b: b, done: make(chan struct{})}
	b.recorder = r
	b.capture.clear()
	b.epoch++
	return r, nil
}
func (b *Bridge) OpenPlayer(config audio.CodecConfig) (audio.DeafenPlayer, error) {
	if config.SampleRate != sampleRate || config.Channels != 1 || config.SamplesPerFrame != frameSamples {
		return nil, errFormat
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	p := &player{b: b}
	b.player = p
	b.playback.clear()
	return p, nil
}

type recorder struct {
	b      *Bridge
	closed bool
	done   chan struct{}
	next   time.Time
}

func (r *recorder) Read(samples []int16) (int, error) {
	b := r.b
	b.mu.Lock()
	defer b.mu.Unlock()
	for !r.closed {
		now := time.Now()
		if delay := r.next.Sub(now); delay > 0 {
			// Spread PCM from multi-frame RTP packets across the core's 20 ms cadence.
			b.mu.Unlock()
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-r.done:
				timer.Stop()
			}
			b.mu.Lock()
			continue
		}
		if next, ok := b.capture.take(now); ok {
			if now.Sub(r.next) >= frameTime {
				r.next = now
			}
			r.next = r.next.Add(frameTime)
			return copy(samples, next.samples[:]), nil
		}
		b.ready.Wait()
	}
	return 0, io.EOF
}
func (r *recorder) Close() error {
	b := r.b
	b.mu.Lock()
	defer b.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	close(r.done)
	if b.recorder == r {
		b.recorder = nil
		b.capture.clear()
		b.epoch++
	}
	b.ready.Broadcast()
	return nil
}

type player struct {
	b *Bridge
}

func (p *player) Write(samples []int16) error {
	b := p.b
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.player == p && b.peer != 0 && !b.deafened && !b.moving {
		f := frame{at: time.Now()}
		copy(f.samples[:], samples)
		b.playback.put(f)
	}
	return nil
}
func (p *player) SetDeafened(value bool) error {
	b := p.b
	b.sinkMu.Lock()
	defer b.sinkMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.player == p {
		b.deafened = value
		b.playback.clear()
		b.playbackEpoch++
	}
	return nil
}
func (p *player) Close() error {
	b := p.b
	b.sinkMu.Lock()
	defer b.sinkMu.Unlock()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.player == p {
		b.player = nil
		b.clearLocked()
	}
	return nil
}
