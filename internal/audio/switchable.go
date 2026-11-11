package audio

import (
	"errors"
	"io"
	"log"
	"sync"
	"time"
)

type captureDevice interface {
	Read(samples []int16) (int, error)
	Close() error
}

// exclusiveCapture is implemented by devices that may hold their endpoint in
// exclusive mode. No second stream can open that endpoint meanwhile.
type exclusiveCapture interface {
	Exclusive() bool
}

// exclusiveBuffered reports the buffer an exclusive stream opened with, so a
// changed ExclusiveCaptureBufferMS can reopen it.
type exclusiveBuffered interface {
	ExclusiveBufferMS() int
}

type SwitchableRecorder struct {
	config CodecConfig

	mu             sync.Mutex
	cond           *sync.Cond
	recorder       captureDevice
	deviceID       string
	generation     uint64
	closed         bool
	open           func(CodecConfig, string) (captureDevice, error)
	onAvailability func(bool)
}

func NewSwitchableRecorder(config CodecConfig, deviceID string) (*SwitchableRecorder, error) {
	return newSwitchableRecorder(config, deviceID, openMalgoCapture)
}

func openMalgoCapture(config CodecConfig, deviceID string) (captureDevice, error) {
	return NewMalgoRecorderForDevice(config, deviceID)
}

func newSwitchableRecorder(config CodecConfig, deviceID string, open func(CodecConfig, string) (captureDevice, error)) (*SwitchableRecorder, error) {
	recorder := &SwitchableRecorder{config: config, deviceID: deviceID, open: open}
	recorder.cond = sync.NewCond(&recorder.mu)
	device, err := open(config, deviceID)
	if err != nil {
		return recorder, err
	}
	recorder.recorder = device
	return recorder, nil
}

func (r *SwitchableRecorder) Available() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return !r.closed && r.recorder != nil
}

func (r *SwitchableRecorder) SetAvailabilityHandler(handler func(bool)) {
	r.mu.Lock()
	r.onAvailability = handler
	r.mu.Unlock()
}

func (r *SwitchableRecorder) Read(samples []int16) (int, error) {
	for {
		r.mu.Lock()
		for !r.closed && r.recorder == nil {
			r.cond.Wait()
		}
		if r.closed || r.recorder == nil {
			r.mu.Unlock()
			return 0, io.EOF
		}
		recorder := r.recorder
		generation := r.generation
		r.mu.Unlock()

		if r.reopenForBuffer(recorder, generation) {
			continue
		}
		n, err := recorder.Read(samples)
		if err == nil {
			return n, nil
		}

		r.mu.Lock()
		replaced := !r.closed && generation != r.generation
		if replaced {
			r.mu.Unlock()
			continue
		}
		if r.closed {
			r.mu.Unlock()
			return 0, io.EOF
		}
		failed := r.recorder
		r.recorder = nil
		handler := r.onAvailability
		r.mu.Unlock()
		if failed != nil {
			_ = failed.Close()
		}
		log.Printf("capture device failed: %v", err)
		if handler != nil {
			handler(false)
		}
	}
}

// reopenForBuffer reopens an exclusive device whose buffer differs from the
// current ExclusiveCaptureBufferMS. The endpoint admits one exclusive stream,
// so the old device is closed before the new one opens. It reports whether the
// caller must pick up the recorder again.
func (r *SwitchableRecorder) reopenForBuffer(device captureDevice, generation uint64) bool {
	held, ok := device.(exclusiveBuffered)
	if !ok || held.ExclusiveBufferMS() == 0 || held.ExclusiveBufferMS() == ExclusiveCaptureBufferMS() {
		return false
	}
	r.mu.Lock()
	if r.closed || r.generation != generation {
		r.mu.Unlock()
		return true
	}
	config, deviceID, open := r.config, r.deviceID, r.open
	r.recorder = nil
	r.generation++
	handler := r.onAvailability
	r.mu.Unlock()
	_ = device.Close()

	next, err := open(config, deviceID)
	r.mu.Lock()
	if r.closed || r.recorder != nil {
		// Closed or switched to another device meanwhile.
		r.mu.Unlock()
		if next != nil {
			_ = next.Close()
		}
		return true
	}
	if err != nil {
		r.mu.Unlock()
		log.Printf("capture reopen for exclusive buffer failed: device=%q buffer_ms=%d error=%v", deviceID, ExclusiveCaptureBufferMS(), err)
		if handler != nil {
			handler(false)
		}
		return true
	}
	r.recorder = next
	r.generation++
	r.cond.Broadcast()
	r.mu.Unlock()
	log.Printf("capture reopened for exclusive buffer: device=%q buffer_ms=%d", deviceID, ExclusiveCaptureBufferMS())
	return true
}

func (r *SwitchableRecorder) Switch(deviceID string) error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return io.ErrClosedPipe
	}
	if r.deviceID == deviceID && r.recorder != nil {
		r.mu.Unlock()
		return nil
	}
	config := r.config
	open := r.open
	r.mu.Unlock()

	next, err := open(config, deviceID)
	if err != nil {
		next, err = r.openAfterReleasingExclusive(config, deviceID, open, err)
		if err != nil {
			return err
		}
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = next.Close()
		return io.ErrClosedPipe
	}
	previous := r.recorder
	r.recorder = next
	r.deviceID = deviceID
	r.generation++
	handler := r.onAvailability
	r.cond.Broadcast()
	r.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	if handler != nil {
		handler(true)
	}
	return nil
}

// openAfterReleasingExclusive handles a switch that failed while the current
// device holds an endpoint exclusively, for example the default microphone and
// the same microphone picked by id. The current device is closed, the new one
// is opened again, and on failure the previous device is restored.
func (r *SwitchableRecorder) openAfterReleasingExclusive(config CodecConfig, deviceID string, open func(CodecConfig, string) (captureDevice, error), openErr error) (captureDevice, error) {
	r.mu.Lock()
	previous := r.recorder
	held, ok := previous.(exclusiveCapture)
	if r.closed || !ok || !held.Exclusive() {
		r.mu.Unlock()
		return nil, openErr
	}
	previousID := r.deviceID
	r.recorder = nil
	r.generation++
	handler := r.onAvailability
	r.mu.Unlock()
	_ = previous.Close()

	next, err := open(config, deviceID)
	if err == nil {
		return next, nil
	}
	log.Printf("capture switch after releasing exclusive device failed: device=%q error=%v", deviceID, err)
	switchErr := errors.Join(openErr, err)

	restored, restoreErr := open(config, previousID)
	if restoreErr != nil {
		log.Printf("capture device restore failed: device=%q error=%v", previousID, restoreErr)
		if handler != nil {
			handler(false)
		}
		return nil, errors.Join(switchErr, restoreErr)
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		_ = restored.Close()
		return nil, io.ErrClosedPipe
	}
	r.recorder = restored
	r.generation++
	r.cond.Broadcast()
	r.mu.Unlock()
	return nil, switchErr
}

func (r *SwitchableRecorder) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	r.generation++
	recorder := r.recorder
	r.recorder = nil
	r.cond.Broadcast()
	r.mu.Unlock()
	if recorder == nil {
		return nil
	}
	return recorder.Close()
}

type SwitchablePlayer struct {
	config CodecConfig

	mu       sync.Mutex
	player   *MalgoPlayer
	deviceID string
	deafened bool
	closed   bool
}

func NewSwitchablePlayer(config CodecConfig, deviceID string) (*SwitchablePlayer, error) {
	player, err := NewMalgoPlayer(config, deviceID)
	if err != nil {
		return nil, err
	}
	return &SwitchablePlayer{config: config, player: player, deviceID: deviceID}, nil
}

func (p *SwitchablePlayer) Write(samples []int16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return io.ErrClosedPipe
	}
	if p.player == nil {
		return nil
	}
	return p.player.Write(samples)
}

func (p *SwitchablePlayer) PlaybackStats() (time.Duration, time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.player == nil {
		return 0, -1
	}
	return p.player.PlaybackStats()
}

func (p *SwitchablePlayer) SetDeafened(value bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return io.ErrClosedPipe
	}
	p.deafened = value
	if p.player == nil {
		return nil
	}
	return p.player.SetDeafened(value)
}

func (p *SwitchablePlayer) Switch(deviceID string) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return io.ErrClosedPipe
	}
	if p.deviceID == deviceID && p.player != nil {
		p.mu.Unlock()
		return nil
	}
	previous := p.player
	oldID := p.deviceID
	config := p.config
	p.mu.Unlock()

	next, err := NewMalgoPlayer(config, deviceID)
	if err != nil && previous != nil {
		// Some endpoints, including Remote Desktop audio, accept only one
		// client. Release the current stream and try the new device again.
		p.mu.Lock()
		if p.player == previous {
			p.player = nil
		}
		p.mu.Unlock()
		_ = previous.Close()
		previous = nil
		next, err = NewMalgoPlayer(config, deviceID)
		if err != nil {
			restored, restoreErr := NewMalgoPlayer(config, oldID)
			p.mu.Lock()
			if p.closed {
				p.mu.Unlock()
				if restored != nil {
					_ = restored.Close()
				}
				return io.ErrClosedPipe
			}
			if restored != nil {
				if deafErr := restored.SetDeafened(p.deafened); deafErr != nil {
					log.Printf("restore playback deafen: %v", deafErr)
				}
				p.player = restored
				p.deviceID = oldID
			}
			p.mu.Unlock()
			if restoreErr != nil {
				log.Printf("restore playback device %q: %v", oldID, restoreErr)
			}
			return err
		}
	}
	if err != nil {
		return err
	}
	p.mu.Lock()
	deafened := p.deafened
	p.mu.Unlock()
	if err := next.SetDeafened(deafened); err != nil {
		_ = next.Close()
		return err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = next.Close()
		return io.ErrClosedPipe
	}
	replaced := p.player
	p.player = next
	p.deviceID = deviceID
	p.mu.Unlock()
	if replaced != nil {
		_ = replaced.Close()
	}
	return nil
}

func (p *SwitchablePlayer) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	player := p.player
	p.player = nil
	p.mu.Unlock()
	if player == nil {
		return nil
	}
	return player.Close()
}

var _ Recorder = (*SwitchableRecorder)(nil)
var _ DeafenPlayer = (*SwitchablePlayer)(nil)
