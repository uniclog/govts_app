package nativeaudio

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"

	"uniclog.io/sonoryx/internal/audio"
)

const deviceChangeTimeout = 10 * time.Second

type DeviceSelection struct {
	CaptureID  string
	PlaybackID string
}

type deviceChange struct {
	selection DeviceSelection
	capture   bool
	done      chan error
}

// Controller owns audio device preferences and all desktop preview streams.
type Controller struct {
	ctx         context.Context
	cancel      context.CancelFunc
	logger      *log.Logger
	pipeline    previewPipeline
	listDevices func() (audio.DeviceList, error)
	changes     chan deviceChange
	changeMu    sync.Mutex
	mu          sync.Mutex
	selection   DeviceSelection

	previewMu          sync.Mutex
	previewWanted      bool
	preview            *micPreview
	monitor            *SwitchablePlayer
	soundPreviewMu     sync.Mutex
	soundPreviewCancel context.CancelFunc
	soundPreviewRun    sync.Mutex
}

func NewController(logger *log.Logger) *Controller {
	ctx, cancel := context.WithCancel(context.Background())
	return &Controller{ctx: ctx, cancel: cancel, logger: logger, listDevices: ListDevices, changes: make(chan deviceChange)}
}

// AttachPipeline wires the desktop devices to client DSP before the app starts.
func (d *Controller) AttachPipeline(pipeline previewPipeline) { d.pipeline = pipeline }

func (d *Controller) NewSession() audio.Session {
	return &deviceSession{controller: d, selection: d.DeviceSelection()}
}

type deviceSession struct {
	controller *Controller
	selection  DeviceSelection
	recorder   *SwitchableRecorder
	player     *SwitchablePlayer
}

func (s *deviceSession) OpenRecorder(config audio.CodecConfig) (audio.Recorder, error) {
	recorder, err := NewSwitchableRecorder(config, s.selection.CaptureID)
	if recorder == nil {
		return nil, err
	}
	s.recorder = recorder
	return recorder, err
}

func (s *deviceSession) OpenPlayer(config audio.CodecConfig) (audio.DeafenPlayer, error) {
	player, err := NewSwitchablePlayer(config, s.selection.PlaybackID)
	if err != nil {
		return nil, err
	}
	s.player = player
	return player, nil
}

func (s *deviceSession) RunDeviceChanges(ctx context.Context) error {
	current := s.selection
	// Preferences may have changed while this session was opening its streams.
	selection := s.controller.DeviceSelection()
	if selection != current && ctx.Err() == nil {
		if err := s.applySelection(&current, selection, true, true); err != nil {
			s.controller.logger.Printf("apply audio device during session startup: %v", err)
		}
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case request := <-s.controller.changes:
			if err := ctx.Err(); err != nil {
				request.done <- err
				return err
			}
			request.done <- s.applySelection(&current, request.selection, request.capture, !request.capture)
		}
	}
}

func (s *deviceSession) applySelection(current *DeviceSelection, next DeviceSelection, capture, playback bool) error {
	s.controller.logger.Printf("audio device switch: old_capture=%q new_capture=%q old_playback=%q new_playback=%q", current.CaptureID, next.CaptureID, current.PlaybackID, next.PlaybackID)
	var captureErr, playbackErr error
	if capture && (next.CaptureID != current.CaptureID || !s.recorder.Available()) {
		captureErr = s.recorder.Switch(next.CaptureID)
		if captureErr == nil {
			current.CaptureID = next.CaptureID
		}
	} else if playback && !s.recorder.Available() {
		// A missing microphone must not block a playback change.
		if err := s.recorder.Switch(current.CaptureID); err != nil {
			s.controller.logger.Printf("audio capture retry failed: capture=%q error=%v", current.CaptureID, err)
		}
	}
	if playback && next.PlaybackID != current.PlaybackID {
		playbackErr = s.player.Switch(next.PlaybackID)
		if playbackErr == nil {
			current.PlaybackID = next.PlaybackID
		}
	}
	err := errors.Join(captureErr, playbackErr)
	s.controller.logger.Printf("audio device switch completed: error=%v", err)
	return err
}

func (d *Controller) AudioDevices() (audio.DeviceList, error) { return d.listDevices() }

func (d *Controller) DeviceSelection() DeviceSelection {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.selection
}

func (d *Controller) SetCaptureDevice(id string) error {
	return d.setAudioDevice(id, true)
}

func (d *Controller) SetPlaybackDevice(id string) error {
	return d.setAudioDevice(id, false)
}

func (d *Controller) setAudioDevice(id string, capture bool) error {
	if err := d.ctx.Err(); err != nil {
		return err
	}
	d.changeMu.Lock()
	defer d.changeMu.Unlock()

	devices, err := d.listDevices()
	if err != nil {
		return err
	}
	available := devices.Playback
	if capture {
		available = devices.Capture
	}
	if id != "" {
		found := false
		for _, device := range available {
			if device.ID == id {
				found = true
				break
			}
		}
		if !found {
			return errors.New("audio device is no longer available")
		}
	}

	d.mu.Lock()
	selection := d.selection
	changed := (capture && selection.CaptureID != id) || (!capture && selection.PlaybackID != id)
	active, connected, captureAvailable := d.pipeline.AudioConnectionState()
	retryCapture := capture && active && connected && !captureAvailable
	if !changed && !retryCapture {
		d.mu.Unlock()
		return nil
	}
	if capture {
		selection.CaptureID = id
	} else {
		selection.PlaybackID = id
	}

	applyInSession := active && connected
	if !applyInSession {
		d.selection = selection
	}
	d.mu.Unlock()

	if applyInSession {
		request := deviceChange{selection: selection, capture: capture, done: make(chan error, 1)}
		timer := time.NewTimer(deviceChangeTimeout)
		defer timer.Stop()
		select {
		case d.changes <- request:
		case <-timer.C:
			return errors.New("timed out while applying audio device")
		case <-d.ctx.Done():
			return context.Canceled
		}
		// A native switch that has started cannot be rolled back at a timeout.
		// Wait for its result before saving the selected device.
		select {
		case err := <-request.done:
			if err != nil {
				return err
			}
		case <-d.ctx.Done():
			return context.Canceled
		}
		d.mu.Lock()
		d.selection = selection
		d.mu.Unlock()
	}
	if changed {
		d.pipeline.LogAudioDeviceChange()
		d.restartPreview(capture)
	}
	return nil
}
