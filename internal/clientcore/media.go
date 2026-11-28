package clientcore

import (
	"context"
	"errors"
	"net/netip"

	voiceclient "uniclog.io/sonoryx/internal/clientcore/internal/live"
	"uniclog.io/sonoryx/internal/clientcore/internal/screensignal"
)

type MediaPublishResult = screensignal.MediaPublishResult
type MediaSubscribeResult = screensignal.MediaSubscribeResult

// MediaSignaling binds all operations on the returned client to the expected voice session.
func (a *App) MediaSignaling(expected uint64) (*screensignal.Client, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	conn, session := a.currentConn, a.chatSession
	if a.closed || conn == nil || session == nil || session.ctx.Err() != nil || a.state.ConnectionStatus() != voiceclient.ConnectionConnected || !a.serverEndpoint.IsValid() {
		return nil, ErrNotConnected
	}
	if expected != a.state.SessionID() {
		return nil, errors.New("media-key-confirmation-expired")
	}
	port := a.state.Snapshot().Info.MediaPort
	if port == 0 {
		return nil, errors.New("screen sharing is disabled on this server")
	}
	endpoint := netip.AddrPortFrom(a.serverEndpoint.Addr(), port)
	return screensignal.New(session.ctx, endpoint, expected, func(ctx context.Context) ([32]byte, error) {
		if !a.beginSessionRequest(session) {
			return [32]byte{}, ErrNotConnected
		}
		defer session.requests.Done()
		return voiceclient.RequestMediaCredential(ctx, conn, a.state)
	}, a.logger), nil
}

// WithCurrentSession applies a local change while the expected voice session is current.
// The callback must not call back into App.
func (a *App) WithCurrentSession(expected uint64, apply func()) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.chatSession == nil || a.chatSession.ctx.Err() != nil || expected != a.state.SessionID() || a.state.ConnectionStatus() != voiceclient.ConnectionConnected {
		return false
	}
	apply()
	return true
}

func (a *App) StopScreen(ctx context.Context, session uint64, streamID, pin string) error {
	signaling, err := a.MediaSignaling(session)
	if err != nil {
		return err
	}
	// The state event skips the owner. Play immediately before the media round trip.
	a.state.PlayNotification(voiceclient.NotificationScreenStopped)
	return signaling.Stop(ctx, streamID, pin)
}
