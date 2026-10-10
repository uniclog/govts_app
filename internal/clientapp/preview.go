package clientapp

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"uniclog.io/govts/internal/audio"
	"uniclog.io/govts/internal/audio/voicegate"
	voiceclient "uniclog.io/govts/internal/client"
)

// The microphone preview runs the capture pipeline (filter, VAD, voice gate)
// while no session is active, so the settings meter shows what would be
// transmitted before joining a server. Encoded output is never produced.
type micPreview struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// previewSink stands in for the Opus encoder: EncodeLoopWithPipeline hands it
// exactly the frames the voice gate releases, which the monitor plays back.
type previewSink struct{ player audio.Player }

func (s previewSink) Encode(samples []int16) ([]byte, error) {
	if s.player != nil {
		return nil, s.player.Write(samples)
	}
	return nil, nil
}

// SetMicrophonePreview requests the preview; it only runs while disconnected
// and stops for the duration of a connection.
func (a *App) SetMicrophonePreview(enabled bool) {
	a.previewMu.Lock()
	defer a.previewMu.Unlock()
	a.previewWanted = enabled
	if !enabled {
		a.previewMonitor = false
	}
	a.syncPreviewLocked()
}

// SetMicrophoneMonitor plays the preview's transmitted audio back on the
// playback device so the user hears what others would hear.
func (a *App) SetMicrophoneMonitor(enabled bool) {
	a.previewMu.Lock()
	defer a.previewMu.Unlock()
	if a.previewMonitor == enabled {
		return
	}
	a.previewMonitor = enabled
	a.stopPreviewLocked()
	a.syncPreviewLocked()
}

func (a *App) syncPreview() {
	a.previewMu.Lock()
	defer a.previewMu.Unlock()
	a.syncPreviewLocked()
}

// restartPreview reopens devices after a selection change; playback only
// matters while the monitor is on.
func (a *App) restartPreview(capture bool) {
	a.previewMu.Lock()
	defer a.previewMu.Unlock()
	if !capture && !a.previewMonitor {
		return
	}
	a.stopPreviewLocked()
	a.syncPreviewLocked()
}

func (a *App) syncPreviewLocked() {
	a.mu.Lock()
	run := a.previewWanted && !a.active && !a.closed
	a.mu.Unlock()
	if !run {
		a.stopPreviewLocked()
		return
	}
	if a.preview != nil {
		return
	}
	ctx, cancel := context.WithCancel(a.lifetimeCtx)
	preview := &micPreview{cancel: cancel, done: make(chan struct{})}
	a.preview = preview
	devices := a.AudioDeviceSelection()
	monitor := a.previewMonitor
	go func() {
		defer close(preview.done)
		if err := a.runPreview(ctx, devices, monitor); err != nil && !errors.Is(err, context.Canceled) {
			a.logger.Printf("microphone preview: %v", err)
		}
	}()
}

func (a *App) stopPreviewLocked() {
	if a.preview == nil {
		return
	}
	a.preview.cancel()
	<-a.preview.done
	a.preview = nil
}

func (a *App) runPreview(ctx context.Context, devices AudioDeviceSelection, monitor bool) (runErr error) {
	codecConfig := clientAudioConfig()
	filter, err := newMicrophoneFilter(codecConfig, a.state.Audio)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, wrapError("close microphone filter", filter.Close())) }()
	detector, err := newMicrophoneVAD(codecConfig)
	if err != nil {
		return err
	}
	defer func() { runErr = errors.Join(runErr, wrapError("close microphone VAD", detector.Close())) }()
	gate, err := voicegate.New(a.state.Audio.VADSnapshot().GateConfig())
	if err != nil {
		return fmt.Errorf("create microphone voice gate: %w", err)
	}
	recorder, err := audio.NewSwitchableRecorder(codecConfig, devices.CaptureID)
	if recorder == nil {
		return fmt.Errorf("create audio recorder: %w", err)
	}

	sink := previewSink{}
	if monitor {
		player, err := audio.NewSwitchablePlayer(codecConfig, devices.PlaybackID)
		if err != nil {
			_ = recorder.Close()
			return fmt.Errorf("create audio player: %w", err)
		}
		defer func() { runErr = errors.Join(runErr, wrapError("close audio player", player.Close())) }()
		// Attaching applies the current deafen state, as in a session.
		detach, err := a.state.Audio.AttachPlayer(player)
		if err != nil {
			_ = recorder.Close()
			return err
		}
		defer detach()
		sink.player = player
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pcmCh := make(chan audio.PCMFrame)
	audioCh := make(chan audio.Frame)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		errs <- voiceclient.EncodeLoopWithPipeline(ctx, sink, filter, detector, gate, pcmCh, audioCh, a.state)
	}()
	go func() {
		defer wg.Done()
		errs <- voiceclient.RecordLoop(ctx, recorder, pcmCh, codecConfig.SamplesPerFrame, a.state.Audio)
	}()
	go func() {
		defer wg.Done()
		for range audioCh {
		}
	}()
	select {
	case <-ctx.Done():
	case runErr = <-errs:
	}
	cancel()
	// Read blocks inside the device until the recorder is closed.
	closeErr := recorder.Close()
	wg.Wait()
	return errors.Join(runErr, wrapError("close audio recorder", closeErr))
}
