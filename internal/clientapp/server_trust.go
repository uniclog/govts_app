package clientapp

import (
	"context"
	"errors"

	"uniclog.io/sonoryx/internal/identity"
)

type ServerIdentityChange struct {
	Endpoint            string `json:"endpoint"`
	PreviousFingerprint string `json:"previousFingerprint"`
	NewFingerprint      string `json:"newFingerprint"`
}

type pendingServerKey struct {
	change   identity.ServerKeyChangedError
	ctx      context.Context
	accepted chan struct{}
}

func (a *App) waitForServerKeyTrust(ctx context.Context, change identity.ServerKeyChangedError) bool {
	pending := &pendingServerKey{change: change, ctx: ctx, accepted: make(chan struct{}, 1)}
	a.mu.Lock()
	a.pendingServerKey = pending
	a.mu.Unlock()
	a.events.append("connection", change.Error(), 0)
	defer func() {
		a.mu.Lock()
		if a.pendingServerKey == pending {
			a.pendingServerKey = nil
		}
		a.mu.Unlock()
	}()
	select {
	case <-ctx.Done():
		return false
	case <-pending.accepted:
		return ctx.Err() == nil
	}
}

func (a *App) ServerIdentityChange() *ServerIdentityChange {
	a.mu.Lock()
	defer a.mu.Unlock()
	pending := a.pendingServerKey
	if pending == nil || pending.ctx.Err() != nil {
		return nil
	}
	return &ServerIdentityChange{
		Endpoint:            pending.change.Endpoint,
		PreviousFingerprint: identity.KeyFingerprint(pending.change.Previous),
		NewFingerprint:      identity.KeyFingerprint(pending.change.NewKey),
	}
}

func (a *App) TrustServerIdentity(endpoint, fingerprint string) error {
	a.mu.Lock()
	pending := a.pendingServerKey
	if pending == nil || pending.ctx.Err() != nil || pending.change.Endpoint != endpoint || identity.KeyFingerprint(pending.change.NewKey) != fingerprint {
		a.mu.Unlock()
		return errors.New("server identity confirmation expired; reconnect before confirming")
	}
	if err := identity.ReplaceServerKey(a.pinsPath, pending.change); err != nil {
		a.mu.Unlock()
		return err
	}
	a.pendingServerKey = nil
	pending.accepted <- struct{}{}
	a.mu.Unlock()
	a.events.append("connection", "Новый ключ сервера подтверждён: "+endpoint, 0)
	return nil
}
