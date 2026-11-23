package clientapp

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	voiceclient "uniclog.io/sonoryx/internal/client"
	"uniclog.io/sonoryx/internal/identity"
)

func TestWatchFailureStates(t *testing.T) {
	rejected := func(reason string) error { return &voiceclient.AuthenticationRejectedError{Reason: reason} }
	for want, errs := range map[string][]error{
		WatchUntrusted:   {identity.ErrServerNotPinned, fmt.Errorf("handshake: %w", &identity.ServerKeyChangedError{Endpoint: "x"})},
		WatchUnsupported: {&voiceclient.ServerVersionTooOldError{}, rejected("invalid authentication init"), rejected("client upgrade required")},
		WatchPassword:    {rejected("server-password-required"), rejected("server-password-incorrect")},
		WatchAccount:     {rejected("watch requires an existing account")},
		WatchBanned:      {rejected("account is banned")},
		WatchUnavailable: {rejected("server is full"), rejected("too many authentication attempts; retry later"), errors.New("secure handshake phase 17 timed out")},
	} {
		for _, err := range errs {
			if got := watchFailure(err); got != want {
				t.Errorf("watchFailure(%v) = %q, want %q", err, got, want)
			}
		}
	}
}

func TestWatchTargetsFollowTheList(t *testing.T) {
	directory := t.TempDir()
	app := New(Options{IdentityPath: filepath.Join(directory, "client.seed"), PinsPath: filepath.Join(directory, "pins.json")})
	defer app.stopWatches()
	// Invalid addresses never start a watcher; nothing listens on port 9, so
	// the valid one stays in a retrying state.
	app.SetWatchTargets("", []WatchTarget{{Address: "not an address"}, {Address: "127.0.0.1:9"}})
	states := app.WatchStates()
	if len(states) != 1 {
		t.Fatalf("watch states = %v, want one target", states)
	}
	if _, ok := states["127.0.0.1:9"]; !ok {
		t.Fatalf("watch states = %v, want 127.0.0.1:9", states)
	}
	app.SetWatchTargets("", nil)
	if states := app.WatchStates(); len(states) != 0 {
		t.Fatalf("watch states after removal = %v", states)
	}
}
