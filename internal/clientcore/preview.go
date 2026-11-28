package clientcore

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"uniclog.io/sonoryx/internal/audio"
	"uniclog.io/sonoryx/internal/audio/voicegate"
	voiceclient "uniclog.io/sonoryx/internal/clientcore/internal/live"
)

// previewSink stands in for the Opus encoder; the preview never sends.
type previewSink struct{}

func (previewSink) Encode([]int16) ([]byte, error) { return nil, nil }

func (a *App) RunMicrophonePreview(ctx context.Context, open func(audio.CodecConfig) (audio.Recorder, error)) (runErr error) {
	codecConfig := audio.DefaultCodecConfig()
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
	recorder, err := open(codecConfig)
	if recorder == nil {
		return fmt.Errorf("create audio recorder: %w", err)
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
		errs <- voiceclient.EncodeLoopWithPipeline(ctx, previewSink{}, filter, detector, gate, pcmCh, audioCh, a.state)
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
