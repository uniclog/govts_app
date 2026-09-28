package audio

import (
	"io"
	"log"
	"sync"
)

type captureDevice interface {
	Read(samples []int16) (int, error)
	Close() error
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
		return err
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
	if p.closed || p.player == nil {
		return io.ErrClosedPipe
	}
	return p.player.Write(samples)
}

func (p *SwitchablePlayer) SetDeafened(value bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || p.player == nil {
		return io.ErrClosedPipe
	}
	if err := p.player.SetDeafened(value); err != nil {
		return err
	}
	p.deafened = value
	return nil
}

func (p *SwitchablePlayer) Switch(deviceID string) error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return io.ErrClosedPipe
	}
	if p.deviceID == deviceID {
		p.mu.Unlock()
		return nil
	}
	config := p.config
	deafened := p.deafened
	p.mu.Unlock()

	next, err := NewMalgoPlayer(config, deviceID)
	if err != nil {
		return err
	}
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
	previous := p.player
	p.player = next
	p.deviceID = deviceID
	p.mu.Unlock()
	_ = previous.Close()
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
