package nativeaudio

import (
	"context"
	"errors"
	"fmt"

	"uniclog.io/sonoryx/internal/audio"
)

type previewPipeline interface {
	AudioConnectionState() (active, connected, captureAvailable bool)
	RunMicrophonePreview(context.Context, func(audio.CodecConfig) (audio.Recorder, error)) error
	SetMonitor(func([]int16))
	SoundPreviewSettings() (string, float32)
	PlaySoundPreviewInSession(context.Context, string) bool
	RenderSoundPreview(string, float32) [][]int16
	LogAudioDeviceChange()
}

// The microphone preview runs the capture pipeline (filter, VAD, voice gate)
// while no session is active, so the settings meter shows what would be
// transmitted before joining a server. Encoded output is never produced.
type micPreview struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// SetMicrophonePreview requests the preview; it only runs while disconnected
// and stops for the duration of a connection. Disabling it also stops the
// monitor, so leaving the microphone settings always resumes transmission.
func (d *Controller) SetMicrophonePreview(enabled bool) {
	d.previewMu.Lock()
	defer d.previewMu.Unlock()
	d.previewWanted = enabled
	if !enabled {
		d.stopMonitorLocked()
	}
	d.syncPreviewLocked()
}

// SetMicrophoneMonitor plays the frames that pass the voice gate on the
// playback device. Before joining a server the preview supplies them; in a
// session the capture pipeline does, and transmission is blocked meanwhile.
func (d *Controller) SetMicrophoneMonitor(enabled bool) error {
	d.previewMu.Lock()
	defer d.previewMu.Unlock()
	if !enabled {
		d.stopMonitorLocked()
		return nil
	}
	if d.monitor != nil {
		return nil
	}
	return d.startMonitorLocked()
}

func (d *Controller) startMonitorLocked() error {
	if err := d.ctx.Err(); err != nil {
		return err
	}
	// A separate stream: the session player belongs to the session playback loop, and the
	// system mixer combines both on the same device.
	player, err := NewSwitchablePlayer(audio.DefaultCodecConfig(), d.DeviceSelection().PlaybackID)
	if err != nil {
		return fmt.Errorf("create monitor player: %w", err)
	}
	d.monitor = player
	d.pipeline.SetMonitor(func(samples []int16) { _ = player.Write(samples) })
	return nil
}

func (d *Controller) stopMonitorLocked() {
	if d.monitor == nil {
		return
	}
	d.pipeline.SetMonitor(nil)
	if err := d.monitor.Close(); err != nil {
		d.logger.Printf("close monitor player: %v", err)
	}
	d.monitor = nil
}

func (d *Controller) SyncPreview() {
	d.previewMu.Lock()
	defer d.previewMu.Unlock()
	d.syncPreviewLocked()
}

// restartPreview reopens devices after a selection change.
func (d *Controller) restartPreview(capture bool) {
	d.previewMu.Lock()
	defer d.previewMu.Unlock()
	if !capture {
		if d.monitor != nil {
			if err := d.monitor.Switch(d.DeviceSelection().PlaybackID); err != nil {
				d.logger.Printf("switch monitor player: %v", err)
			}
		}
		return
	}
	d.stopPreviewLocked()
	d.syncPreviewLocked()
}

func (d *Controller) syncPreviewLocked() {
	if !d.previewWanted || d.ctx.Err() != nil {
		d.stopPreviewLocked()
		return
	}
	active, _, _ := d.pipeline.AudioConnectionState()
	if active {
		d.stopPreviewLocked()
		return
	}
	if d.preview != nil {
		return
	}
	ctx, cancel := context.WithCancel(d.ctx)
	preview := &micPreview{cancel: cancel, done: make(chan struct{})}
	d.preview = preview
	devices := d.DeviceSelection()
	go func() {
		defer close(preview.done)
		if err := d.pipeline.RunMicrophonePreview(ctx, func(config audio.CodecConfig) (audio.Recorder, error) {
			recorder, err := NewSwitchableRecorder(config, devices.CaptureID)
			if recorder == nil {
				return nil, err
			}
			return recorder, err
		}); err != nil && !errors.Is(err, context.Canceled) {
			d.logger.Printf("microphone preview: %v", err)
		}
	}()
}

func (d *Controller) stopPreviewLocked() {
	if d.preview == nil {
		return
	}
	d.preview.cancel()
	<-d.preview.done
	d.preview = nil
}

// Connect releases the preview recorder before starting a voice session.
func (d *Controller) Connect(start func() error) error {
	d.previewMu.Lock()
	defer d.previewMu.Unlock()
	d.stopPreviewLocked()
	d.stopMonitorLocked()
	if err := start(); err != nil {
		d.syncPreviewLocked()
		return err
	}
	return nil
}

func (d *Controller) Close() {
	d.cancel()
	d.previewMu.Lock()
	d.stopPreviewLocked()
	d.stopMonitorLocked()
	d.previewMu.Unlock()

	// The sound preview closes its player before releasing this lock.
	d.soundPreviewRun.Lock()
	d.soundPreviewRun.Unlock()
}
