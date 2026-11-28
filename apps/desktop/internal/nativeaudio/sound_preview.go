package nativeaudio

import (
	"context"
	"errors"
	"time"

	"uniclog.io/sonoryx/internal/audio"
)

func (d *Controller) PreviewSoundPack() error {
	if err := d.ctx.Err(); err != nil {
		return err
	}
	d.soundPreviewMu.Lock()
	if d.soundPreviewCancel != nil {
		d.soundPreviewCancel()
	}
	ctx, cancel := context.WithCancel(d.ctx)
	d.soundPreviewCancel = cancel
	d.soundPreviewMu.Unlock()

	d.soundPreviewRun.Lock()
	defer d.soundPreviewRun.Unlock()
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil
	}

	pack, volume := d.pipeline.SoundPreviewSettings()
	if volume <= 0 {
		return nil
	}
	// The session player is already open. A second WASAPI stream on the same
	// endpoint, especially Remote Desktop audio, stalls for the whole open
	// attempt before the first sample.
	if d.pipeline.PlaySoundPreviewInSession(ctx, pack) {
		return nil
	}
	player, err := NewMalgoPlayer(audio.DefaultCodecConfig(), d.DeviceSelection().PlaybackID)
	if err != nil {
		if d.pipeline.PlaySoundPreviewInSession(ctx, pack) {
			return nil
		}
		return err
	}
	defer func() { _ = player.Close() }()

	frame := audio.DefaultCodecConfig()
	frameSamples := frame.SamplesPerFrame * frame.Channels
	if frameSamples <= 0 {
		frameSamples = 960
	}
	for _, samples := range d.pipeline.RenderSoundPreview(pack, volume) {
		if err := writePreviewPCM(ctx, player, samples, frameSamples); err != nil {
			if errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}

func (d *Controller) StopSoundPreview() {
	d.soundPreviewMu.Lock()
	defer d.soundPreviewMu.Unlock()
	if d.soundPreviewCancel != nil {
		d.soundPreviewCancel()
	}
}

func writePreviewPCM(ctx context.Context, player audio.Player, samples []int16, frameSamples int) error {
	if len(samples) == 0 {
		return nil
	}
	written := 0
	for primed := 0; primed < 3 && written < len(samples); primed++ {
		end := written + frameSamples
		if end > len(samples) {
			end = len(samples)
		}
		if err := player.Write(samples[written:end]); err != nil {
			return err
		}
		written = end
	}
	for written < len(samples) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
		end := written + frameSamples
		if end > len(samples) {
			end = len(samples)
		}
		if err := player.Write(samples[written:end]); err != nil {
			return err
		}
		written = end
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(60 * time.Millisecond):
		return nil
	}
}
