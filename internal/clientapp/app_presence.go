package clientapp

import (
	"context"
	"errors"
	"time"

	voiceclient "uniclog.io/sonoryx/internal/client"
)

func (a *App) SetAway(ctx context.Context, away bool) error {
	a.presenceMu.Lock()
	defer a.presenceMu.Unlock()
	_, err := a.setAway(ctx, away)
	return err
}

// ToggleAway flips presence as it stands when the call gets the lock, so a
// press decided earlier cannot overwrite a click that already landed.
func (a *App) ToggleAway(ctx context.Context) (changed, away bool, err error) {
	a.presenceMu.Lock()
	defer a.presenceMu.Unlock()
	next := !a.currentAway()
	changed, err = a.setAway(ctx, next)
	if err != nil || !changed {
		return false, false, err
	}
	return true, next, nil
}

func (a *App) currentAway() bool {
	if a.awayTracked {
		return a.awayNow
	}
	id := a.state.SessionID()
	for _, participant := range a.state.Snapshot().Participants {
		if participant.SessionID == id {
			return participant.Away
		}
	}
	return false
}

func (a *App) clearAway() {
	a.presenceMu.Lock()
	a.awayTracked = false
	a.presenceMu.Unlock()
}

func (a *App) setAway(ctx context.Context, away bool) (bool, error) {
	// A Back click during the away cue waits here instead of racing the
	// deafen that Away is about to make.
	a.mu.Lock()
	conn := a.currentConn
	a.mu.Unlock()
	if conn == nil || a.state.ConnectionStatus() != voiceclient.ConnectionConnected {
		return false, ErrNotConnected
	}
	if a.state.ChannelID() == 0 {
		return false, errors.New("presence requires a channel")
	}
	if a.awayTracked && a.awayNow == away {
		return false, nil
	}
	previousTracked, previousNow := a.awayTracked, a.awayNow
	a.awayTracked, a.awayNow = true, away
	published := false
	revert := func() {
		if !published {
			a.awayTracked, a.awayNow = previousTracked, previousNow
		}
	}
	// Play before the presence round trip. The acknowledgement can take a
	// retry interval, and the click should not wait for it.
	if !away {
		// Playback has to be open before the return cue, or deafen swallows it.
		undeafened, err := a.state.Audio.UndeafenForReturn()
		if err != nil {
			revert()
			return false, err
		}
		a.state.PlayNotification(voiceclient.NotificationReturned)
		if err := voiceclient.PublishPresence(ctx, conn, a.state, false); err != nil {
			// The server still shows Away. Keep playback as Away left it.
			if undeafened {
				a.state.Audio.DeafenForAway()
			}
			revert()
			return false, err
		}
		published = true
		return true, a.state.Audio.UnmuteForReturn()
	}
	started := time.Now()
	a.state.PlayNotification(voiceclient.NotificationAway)
	if err := voiceclient.PublishPresence(ctx, conn, a.state, true); err != nil {
		revert()
		return false, err
	}
	published = true
	if a.state.Audio.MuteOnAway() {
		a.state.Audio.MuteForAway()
	}
	if a.state.Audio.DeafenOnAway() {
		// Deafen clears the playback buffer. Wait out the away cue first.
		if lead := a.CueLead(voiceclient.NotificationAway); lead > 0 {
			if waited := time.Since(started); waited < lead {
				timer := time.NewTimer(lead - waited)
				select {
				case <-ctx.Done():
					timer.Stop()
				case <-timer.C:
				}
			}
		}
		a.state.Audio.DeafenForAway()
	}
	return true, nil
}

func (a *App) Wake(ctx context.Context, target uint64) error {
	a.mu.Lock()
	conn := a.currentConn
	a.mu.Unlock()
	if conn == nil || a.state.ConnectionStatus() != voiceclient.ConnectionConnected {
		return ErrNotConnected
	}
	return voiceclient.Attention(ctx, conn, a.state, target)
}
