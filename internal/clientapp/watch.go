package clientapp

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"time"

	"uniclog.io/sonoryx/internal/appversion"
	voiceclient "uniclog.io/sonoryx/internal/client"
	"uniclog.io/sonoryx/internal/identity"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

// Watch states reported to the interface.
const (
	WatchConnecting  = "connecting"
	WatchActive      = "active"
	WatchPassword    = "password"    // the server wants a password we do not have
	WatchAccount     = "account"     // no account yet: connect to the server once
	WatchUntrusted   = "untrusted"   // the key is not pinned or has changed
	WatchUnsupported = "unsupported" // the server predates watchers
	WatchBanned      = "banned"
	WatchUnavailable = "unavailable"
)

const (
	watchHeartbeat    = 10 * time.Second
	watchMissedLimit  = 3
	watchRetryMin     = 5 * time.Second
	watchRetryMax     = 10 * time.Minute
	watchRetryRefused = 15 * time.Minute
)

// WatchTarget is a subscribed server the client is not connected to.
type WatchTarget struct {
	Address  string
	Password string
}

// NoticeSink receives notices from the main session and from watchers.
// watcher reports that the notice came from a background watcher session.
type NoticeSink func(server string, watcher bool, notice protocol.Notice)

type watchEntry struct {
	target WatchTarget
	cancel context.CancelFunc
	done   chan struct{}
}

type watchSet struct {
	mu      sync.Mutex
	name    string
	entries map[string]*watchEntry
	states  map[string]string
}

// SetWatchTargets keeps one background watcher per target and stops the rest.
// name is sent in the handshake; the server does not use it for watchers.
func (a *App) SetWatchTargets(name string, targets []WatchTarget) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		name = "Sonoryx"
	}
	w := &a.watches
	w.mu.Lock()
	if w.entries == nil {
		w.entries, w.states = make(map[string]*watchEntry), make(map[string]string)
	}
	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	wanted := make(map[string]WatchTarget, len(targets))
	if !closed {
		for _, target := range targets {
			endpoint, err := ParseServerEndpoint(target.Address)
			if err != nil {
				continue
			}
			target.Address = endpoint.String()
			wanted[target.Address] = target
		}
	}
	var stopping []*watchEntry
	for address, entry := range w.entries {
		if target, ok := wanted[address]; ok && target == entry.target && name == w.name {
			continue
		}
		entry.cancel()
		stopping = append(stopping, entry)
		delete(w.entries, address)
		delete(w.states, address)
	}
	w.name = name
	for address, target := range wanted {
		if _, running := w.entries[address]; running {
			continue
		}
		ctx, cancel := context.WithCancel(a.lifetimeCtx)
		entry := &watchEntry{target: target, cancel: cancel, done: make(chan struct{})}
		w.entries[address] = entry
		w.states[address] = WatchConnecting
		go a.runWatch(ctx, entry, name)
	}
	w.mu.Unlock()
	for _, entry := range stopping {
		<-entry.done
	}
}

// WatchStates maps each watched address to its state.
func (a *App) WatchStates() map[string]string {
	a.watches.mu.Lock()
	defer a.watches.mu.Unlock()
	out := make(map[string]string, len(a.watches.states))
	for address, state := range a.watches.states {
		out[address] = state
	}
	return out
}

func (a *App) setWatchState(entry *watchEntry, state string) {
	a.watches.mu.Lock()
	changed := false
	if a.watches.entries[entry.target.Address] == entry && a.watches.states[entry.target.Address] != state {
		a.watches.states[entry.target.Address] = state
		changed = true
	}
	a.watches.mu.Unlock()
	if changed && a.watchChanged != nil {
		a.watchChanged()
	}
}

// SetWatchChanged installs a callback for watcher state changes.
func (a *App) SetWatchChanged(callback func()) { a.watchChanged = callback }

func (a *App) runWatch(ctx context.Context, entry *watchEntry, name string) {
	defer close(entry.done)
	endpoint, err := ParseServerEndpoint(entry.target.Address)
	if err != nil {
		return
	}
	retry := watchRetryMin
	// An unreachable server is retried for as long as it is subscribed, so a
	// failure is logged when it changes, not on every attempt.
	lastLogged := ""
	for ctx.Err() == nil {
		state, err := a.watchOnce(ctx, entry, endpoint, name)
		if ctx.Err() != nil {
			return
		}
		a.setWatchState(entry, state)
		delay := retry
		switch state {
		case WatchActive:
			// The session worked; start again from the short delay.
			retry, delay = watchRetryMin, watchRetryMin
		case WatchUnavailable:
			retry = min(retry*2, watchRetryMax)
		default:
			delay = watchRetryRefused
		}
		if summary := state + "|" + errorText(err); summary != lastLogged {
			lastLogged = summary
			a.logger.Printf("watcher stopped: server=%s state=%s retry=%s error=%v", endpoint, state, delay, err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// watchOnce runs one watcher session until it fails. It returns the state to
// show while waiting for the next attempt.
func (a *App) watchOnce(ctx context.Context, entry *watchEntry, endpoint netip.AddrPort, name string) (string, error) {
	private, err := a.loadClientIdentity()
	if err != nil {
		return WatchUnavailable, err
	}
	conn, codec, err := openClientPacketConn(endpoint)
	if err != nil {
		return WatchUnavailable, err
	}
	defer conn.Close()
	a.identityMu.Lock()
	pins := a.pinsPath
	a.identityMu.Unlock()
	result, err := voiceclient.PerformWatchHandshake(ctx, conn, codec, name, private, pins, endpoint.String(), handshakeAttemptTimeout, handshakeAttempts, appversion.MinimumServerVersion, entry.target.Password)
	if err != nil {
		return watchFailure(err), err
	}
	if err := conn.BindSession(result.SessionID); err != nil {
		return WatchUnavailable, err
	}
	a.setWatchState(entry, WatchActive)
	a.logger.Printf("watcher connected: server=%s session_id=%d", endpoint, result.SessionID)
	err = a.serveWatch(ctx, conn, entry.target.Address, result.SessionID)
	if ctx.Err() != nil {
		// Leave politely; the server would otherwise keep the watcher 30 s.
		_ = conn.SendPacket(protocol.VoicePacket{Type: protocol.PacketDisconnect, SessionID: result.SessionID, RequestID: 1})
	}
	return WatchActive, err
}

func watchFailure(err error) string {
	var rejected *voiceclient.AuthenticationRejectedError
	var keyChanged *identity.ServerKeyChangedError
	var tooOld *voiceclient.ServerVersionTooOldError
	switch {
	case errors.Is(err, identity.ErrServerNotPinned), errors.As(err, &keyChanged):
		return WatchUntrusted
	case errors.As(err, &tooOld):
		return WatchUnsupported
	case errors.As(err, &rejected):
		switch rejected.Reason {
		case "server-password-required", "server-password-incorrect":
			return WatchPassword
		case "watch requires an existing account":
			return WatchAccount
		case "account is banned":
			return WatchBanned
		case "invalid authentication init":
			return WatchUnsupported
		}
		if rejected.Temporary() {
			return WatchUnavailable
		}
		return WatchUnsupported
	}
	return WatchUnavailable
}

// serveWatch sends heartbeats and forwards notices until the session ends.
func (a *App) serveWatch(ctx context.Context, conn *udp.ClientPacketConn, server string, sessionID uint64) error {
	stop := context.AfterFunc(ctx, func() { _ = conn.SetReadDeadline(time.Now()) })
	defer stop()
	defer conn.SetReadDeadline(time.Time{})
	var request uint32
	missed := 0
	next := time.Now()
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if now := time.Now(); !now.Before(next) {
			if missed >= watchMissedLimit {
				return errors.New("watcher heartbeat timed out")
			}
			request++
			if err := conn.SendPacket(protocol.VoicePacket{Type: protocol.PacketHeartbeat, SessionID: sessionID, RequestID: request}); err != nil {
				return err
			}
			missed++
			next = now.Add(watchHeartbeat)
		}
		if err := conn.SetReadDeadline(next); err != nil {
			return err
		}
		packet, err := conn.ReceivePacket()
		if err != nil {
			var netErr net.Error
			if (errors.As(err, &netErr) && netErr.Timeout()) || errors.Is(err, protocol.ErrRejectedDatagram) {
				continue
			}
			return err
		}
		if packet.SessionID != sessionID {
			continue
		}
		switch packet.Type {
		case protocol.PacketHeartbeatAck:
			missed = 0
		case protocol.PacketSessionInvalid:
			return errors.New("watcher session expired")
		case protocol.PacketNotice:
			if packet.RequestID != 0 {
				continue
			}
			notice, err := protocol.DecodeNotice(packet.Payload)
			if err != nil {
				a.logger.Printf("watcher notice rejected: server=%s size=%d error=%v", server, len(packet.Payload), err)
				continue
			}
			a.deliverNotice(server, true, notice)
		}
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// stopWatches stops every watcher during shutdown.
func (a *App) stopWatches() {
	a.SetWatchTargets("", nil)
}
