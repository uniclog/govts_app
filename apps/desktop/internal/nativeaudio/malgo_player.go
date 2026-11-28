package nativeaudio

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"
	"uniclog.io/sonoryx/internal/audio"

	"github.com/gen2brain/malgo"
)

type MalgoPlayer struct {
	context *malgo.AllocatedContext
	device  *malgo.Device
	buffer  *playbackBuffer

	mu       sync.Mutex
	closed   bool
	deafened bool

	bytesPerMS      int
	overflowBytes   int
	overflowLogged  time.Time
	overflowStarted time.Time

	// lastCallback is the UnixNano of the device's last data pull.
	lastCallback *atomic.Int64
}

// playbackOverflowLogInterval keeps a steady clock drift to one line per window.
const playbackOverflowLogInterval = 10 * time.Second

func NewMalgoPlayer(config audio.CodecConfig, deviceID string) (*MalgoPlayer, error) {
	buffer := &playbackBuffer{limit: config.SamplesPerFrame * config.Channels * 2 * 5}
	lastCallback := &atomic.Int64{}
	callbacks := malgo.DeviceCallbacks{Data: func(output, _ []byte, _ uint32) {
		buffer.read(output)
		lastCallback.Store(time.Now().UnixNano())
	}}
	context, device, _, err := openMalgoStream(malgo.Playback, config, deviceID, callbacks)
	if err != nil {
		return nil, err
	}
	return &MalgoPlayer{context: context, device: device, buffer: buffer, bytesPerMS: config.SampleRate * config.Channels * 2 / 1000, lastCallback: lastCallback}, nil
}

func (p *MalgoPlayer) PlaybackStats() (time.Duration, time.Duration) {
	var buffered time.Duration
	if p.bytesPerMS > 0 {
		buffered = time.Duration(p.buffer.len()/p.bytesPerMS) * time.Millisecond
	}
	sinceCallback := time.Duration(-1)
	if last := p.lastCallback.Load(); last != 0 {
		sinceCallback = time.Since(time.Unix(0, last))
	}
	return buffered, sinceCallback
}

func (p *MalgoPlayer) Write(samples []int16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return io.ErrClosedPipe
	}
	if !p.deafened {
		if dropped := p.buffer.push(audio.PCMBytes(samples)); dropped > 0 && audio.SoundLogEnabled() {
			p.recordOverflow(dropped)
		}
	}
	return nil
}

// recordOverflow logs audio the device did not take in time. Voice and app
// sounds are cut here when the output consumes slower than the 20 ms mixer.
func (p *MalgoPlayer) recordOverflow(dropped int) {
	now := time.Now()
	if p.overflowBytes == 0 {
		p.overflowStarted = now
	}
	p.overflowBytes += dropped
	if now.Sub(p.overflowLogged) < playbackOverflowLogInterval {
		return
	}
	droppedMS := 0
	if p.bytesPerMS > 0 {
		droppedMS = p.overflowBytes / p.bytesPerMS
	}
	audio.SoundLogf("playback buffer overflow: dropped_ms=%d window=%s", droppedMS, now.Sub(p.overflowStarted).Round(time.Millisecond))
	p.overflowBytes = 0
	p.overflowLogged = now
}

func (p *MalgoPlayer) SetDeafened(value bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return io.ErrClosedPipe
	}
	p.deafened = value
	if value {
		p.buffer.clear()
	}
	return nil
}

func (p *MalgoPlayer) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	p.buffer.clear()
	var stopErr, contextErr error
	if p.device != nil {
		if err := p.device.Stop(); err != nil {
			stopErr = fmt.Errorf("stop playback device: %w", err)
		}
		p.device.Uninit()
	}
	if p.context != nil {
		if err := p.context.Uninit(); err != nil {
			contextErr = fmt.Errorf("uninitialize playback context: %w", err)
		}
		p.context.Free()
	}
	return errors.Join(stopErr, contextErr)
}

var _ audio.DeafenPlayer = (*MalgoPlayer)(nil)
