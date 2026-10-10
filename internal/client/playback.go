package client

import (
	"context"

	"uniclog.io/sonoryx/internal/audio"
)

func PlaybackLoop(
	ctx context.Context,
	player audio.Player,
	pcmOutCh <-chan audio.PCMFrame,
	controls ...*AudioControlState,
) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case frame, ok := <-pcmOutCh:
			if !ok {
				return nil
			}
			write := func() error { return player.Write(frame.Samples) }
			var err error
			if len(controls) > 0 {
				err = controls[0].Play(frame.PlaybackEpoch, write)
			} else {
				err = write()
			}
			if err != nil {
				return err
			}
		}
	}
}
