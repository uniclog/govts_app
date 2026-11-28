package clientcore

import (
	"context"
	"errors"
	"fmt"
	stdlog "log"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	voiceclient "uniclog.io/sonoryx/internal/clientcore/internal/live"
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

func TestReconnectRejoinsPreviousChannel(t *testing.T) {
	snapshot := domain.ServerSnapshot{
		Info:     domain.ServerInfo{DefaultChannelID: 1},
		Channels: []domain.Channel{{ID: 1, Name: "welcome"}, {ID: 2, Name: "team"}},
	}
	var log strings.Builder
	logger := stdlog.New(&log, "", 0)
	for _, item := range []struct {
		name     string
		previous domain.ChannelID
		refuse   error
		joined   []domain.ChannelID
		rejoined bool
		logged   string
	}{
		{name: "first connection", previous: 0},
		{name: "was in default", previous: 1},
		{name: "channel deleted", previous: 9, logged: "previous channel 9 no longer exists"},
		{name: "join refused", previous: 2, refuse: errors.New("channel full"), joined: []domain.ChannelID{2}, logged: "cannot rejoin channel 2: channel full"},
		{name: "rejoined", previous: 2, joined: []domain.ChannelID{2}, rejoined: true},
	} {
		log.Reset()
		var joined []domain.ChannelID
		join := func(id domain.ChannelID) error {
			joined = append(joined, id)
			return item.refuse
		}
		if got := rejoinPreviousChannel(snapshot, item.previous, 1, join, logger); got != item.rejoined {
			t.Errorf("%s: rejoined = %t", item.name, got)
		}
		if !slices.Equal(joined, item.joined) {
			t.Errorf("%s: joins = %v, want %v", item.name, joined, item.joined)
		}
		if item.logged == "" && log.Len() != 0 || !strings.Contains(log.String(), item.logged) {
			t.Errorf("%s: log = %q", item.name, log.String())
		}
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
	app := newTestApp(t)
	if app.waitForReconnect(ctx, time.Hour) {
		t.Fatal("waitForReconnect returned retry after cancellation")
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
