package clientapp

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	voiceclient "uniclog.io/sonoryx/internal/client"
	"uniclog.io/sonoryx/internal/domain"
)

func TestReconnectBackoff(t *testing.T) {
	backoff := newReconnectBackoff()
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second, 4 * time.Second}
	for index, duration := range want {
		if got := backoff.Next(); got != duration {
			t.Fatalf("delay[%d] = %s, want %s", index, got, duration)
		}
	}
	backoff.Reset()
	if got := backoff.Next(); got != time.Second {
		t.Fatalf("delay after reset = %s", got)
	}
}

func TestConnectionAlwaysSelectsMarkedDefaultChannel(t *testing.T) {
	snapshot := domain.ServerSnapshot{
		Info: domain.ServerInfo{DefaultChannelID: 2},
		Channels: []domain.Channel{
			{ID: 1, Name: "private", MinJoinLevel: 25},
			{ID: 2, Name: "welcome"},
		},
	}
	if id, err := defaultChannelForConnection(snapshot); err != nil || id != 2 {
		t.Fatalf("selected channel = %d, %v", id, err)
	}
	snapshot.Channels[1].MaxUsers = 10
	if _, err := defaultChannelForConnection(snapshot); err == nil {
		t.Fatal("limited default channel was accepted")
	}
}

func TestUnusableSocketRequiresReplacement(t *testing.T) {
	err := fmt.Errorf("connection lost: receive: %w", net.ErrClosed)
	if !shouldReplaceClientSocket(err) {
		t.Fatal("closed socket was considered reusable")
	}
	if shouldReplaceClientSocket(context.DeadlineExceeded) {
		t.Fatal("temporary timeout requested socket replacement")
	}
}

func TestWaitForReconnectStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	app := New(Options{})
	if app.waitForReconnect(ctx, time.Hour) {
		t.Fatal("waitForReconnect returned retry after cancellation")
	}
}

func TestConnectionTreeOutputPolicy(t *testing.T) {
	tests := []struct {
		name              string
		first             bool
		alreadyPrinted    bool
		topologyUnchanged bool
		wantFullTree      bool
	}{
		{name: "first connection", first: true, topologyUnchanged: true, wantFullTree: true},
		{name: "join after tree", first: true, alreadyPrinted: true, wantFullTree: false},
		{name: "reconnect unchanged", topologyUnchanged: true, wantFullTree: false},
		{name: "reconnect changed", topologyUnchanged: false, wantFullTree: true},
		{name: "missing channel already showed tree", alreadyPrinted: true, topologyUnchanged: false, wantFullTree: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := shouldRenderConnectionTree(test.first, test.alreadyPrinted, test.topologyUnchanged); got != test.wantFullTree {
				t.Fatalf("shouldRenderConnectionTree() = %t, want %t", got, test.wantFullTree)
			}
		})
	}
}

func TestNetworkLoopDoesNotRepeatConnectionLost(t *testing.T) {
	classified := fmt.Errorf("%w: heartbeat: request timed out", voiceclient.ErrConnectionLost)
	got := classifyNetworkLoopError("heartbeat", classified)
	if got != classified {
		t.Fatalf("classified error was wrapped again: %v", got)
	}
	if strings.Count(got.Error(), voiceclient.ErrConnectionLost.Error()) != 1 {
		t.Fatalf("connection state repeated in %q", got)
	}
}
