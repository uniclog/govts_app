package clientcore

import (
	"context"
	"time"

	voiceclient "uniclog.io/sonoryx/internal/clientcore/internal/live"
)

func (a *App) SoundPreviewSettings() (string, float32) { return a.state.Audio.SoundSnapshot() }

func (a *App) RenderSoundPreview(pack string, volume float32) [][]int16 {
	kinds := voiceclient.PreviewSoundKinds()
	samples := make([][]int16, 0, len(kinds))
	for _, kind := range kinds {
		samples = append(samples, voiceclient.RenderNotificationPCM(pack, volume, kind))
	}
	return samples
}

func (a *App) PlaySoundPreviewInSession(ctx context.Context, pack string) bool {
	_, connected, _ := a.AudioConnectionState()
	deafened, _ := a.state.Audio.PlaybackSnapshot()
	if !connected || deafened {
		return false
	}
	_, volume := a.state.Audio.SoundSnapshot()
	for _, kind := range voiceclient.PreviewSoundKinds() {
		samples := voiceclient.RenderNotificationPCM(pack, volume, kind)
		a.state.PlayPreviewNotification(kind)
		wait := time.Duration(len(samples)) * time.Second / 48000
		if wait < 20*time.Millisecond {
			wait = 20 * time.Millisecond
		}
		wait += 100 * time.Millisecond
		select {
		case <-ctx.Done():
			return true
		case <-time.After(wait):
		}
	}
	return true
}
