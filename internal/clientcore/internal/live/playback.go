package client

import (
	"context"

	"uniclog.io/sonoryx/internal/audio"
)

func PlaybackLoopWithState(ctx context.Context, player audio.Player, pcmOutCh <-chan audio.PCMFrame, state *State) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case frame, ok := <-pcmOutCh:
			if !ok {
				return nil
			}
			written := false
			write := func() error {
				written = true
				return player.Write(frame.Samples)
			}
			err := state.playVoicePCM(frame, write)
			if err != nil {
				return err
			}
			if frame.Cue != 0 && audio.SoundLogEnabled() {
				logCueHandoff(player, NotificationSoundKind(frame.Cue), written)
			}
		}
	}
}

// logCueHandoff is the last step a cue can be traced in this process. A cue
// that reaches the device with a short buffer and a live callback and is still
// not heard was lost by the output driver or device.
func logCueHandoff(player audio.Player, kind NotificationSoundKind, written bool) {
	if !written {
		audio.SoundLogf("sound cue dropped: kind=%s reason=deafened_or_playback_epoch", kind)
		return
	}
	stats, ok := player.(audio.PlaybackStats)
	if !ok {
		audio.SoundLogf("sound cue to device: kind=%s", kind)
		return
	}
	buffered, sinceCallback := stats.PlaybackStats()
	audio.SoundLogf("sound cue to device: kind=%s buffered_ms=%d last_callback_ms=%d", kind, buffered.Milliseconds(), sinceCallback.Milliseconds())
}
