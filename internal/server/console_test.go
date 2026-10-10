package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/voice"
)

func TestConsoleCommandsShowDeterministicState(t *testing.T) {
	hub, mainID, now := consoleTestHub(t)
	info := ConsoleInfo{
		StartedAt:     now.Add(-time.Minute),
		ListenAddress: ":9000",
		ConfigSource:  "test.json",
	}

	tests := []struct {
		name    string
		command string
		want    string
	}{
		{
			name:    "status",
			command: "status",
			want:    fmt.Sprintf("status server=%q uptime=1m0s listen=\":9000\" config=\"test.json\" revision=5 channels=2 users=2 unjoined=1\n", voice.DefaultServerName),
		},
		{
			name:    "channels",
			command: "channels",
			want: "channels revision=5 count=2\n" +
				"- id=1 name=\"default\" parent=0 position=0 users=0/unlimited default=true min_join_level=0 type=permanent audio=opus/48000Hz/1ch/20ms/24000bps/voip\n" +
				"- id=2 name=\"main\" parent=0 position=10 users=1/unlimited default=false min_join_level=0 type=permanent audio=opus/48000Hz/1ch/20ms/24000bps/voip\n",
		},
		{
			name:    "users",
			command: "users",
			want: "users revision=5 count=2\n" +
				"id=10 name=\"bob\" channel_id=0 channel=\"\" endpoint=\"127.0.0.1:9002\" last_seen=2026-09-11T12:00:00Z idle=1m0s\n" +
				"id=20 name=\"alice\" channel_id=2 channel=\"main\" endpoint=\"127.0.0.1:9001\" last_seen=2026-09-11T12:00:50Z idle=10s\n",
		},
		{
			name:    "channel by name",
			command: "channel main",
			want: "channel id=2 name=\"main\" parent=0 position=10 users=1/unlimited default=false min_join_level=0 type=permanent audio=opus/48000Hz/1ch/20ms/24000bps/voip\n" +
				"  topic=\"Main topic\"\n" +
				"  description=\"Main description\"\n" +
				"  id=20 name=\"alice\" channel_id=2 channel=\"main\" endpoint=\"127.0.0.1:9001\" last_seen=2026-09-11T12:00:50Z idle=10s\n",
		},
		{
			name:    "channel by ID",
			command: "channel " + strconv.FormatUint(uint64(mainID), 10),
			want: "channel id=2 name=\"main\" parent=0 position=10 users=1/unlimited default=false min_join_level=0 type=permanent audio=opus/48000Hz/1ch/20ms/24000bps/voip\n" +
				"  topic=\"Main topic\"\n" +
				"  description=\"Main description\"\n" +
				"  id=20 name=\"alice\" channel_id=2 channel=\"main\" endpoint=\"127.0.0.1:9001\" last_seen=2026-09-11T12:00:50Z idle=10s\n",
		},
		{
			name:    "user",
			command: "user 20",
			want:    "user id=20 name=\"alice\" channel_id=2 channel=\"main\" endpoint=\"127.0.0.1:9001\" last_seen=2026-09-11T12:00:50Z idle=10s\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// Console timestamps intentionally use the machine's local timezone.
			want := strings.ReplaceAll(test.want, "2026-09-11T12:00:00Z", now.Add(-time.Minute).Local().Format(time.RFC3339))
			want = strings.ReplaceAll(want, "2026-09-11T12:00:50Z", now.Add(-10*time.Second).Local().Format(time.RFC3339))
			var output bytes.Buffer
			console := NewConsole(hub, strings.NewReader(""), &output, info)
			console.now = func() time.Time { return now }
			if err := console.Execute(test.command); err != nil {
				t.Fatal(err)
			}
			if got := output.String(); got != want {
				t.Fatalf("output:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestConsoleParserErrorsDoNotFailExecution(t *testing.T) {
	hub, _, now := consoleTestHub(t)
	tests := []struct {
		command string
		want    string
	}{
		{command: "unknown", want: `unknown command "unknown"`},
		{command: "status\textra", want: "usage: status"},
		{command: "channels extra", want: "usage: channels"},
		{command: "channel", want: "usage: channel <id|name>"},
		{command: "channel missing", want: `channel "missing" not found`},
		{command: "user", want: "usage: user <session-id>"},
		{command: "user nope", want: "usage: user <session-id>"},
		{command: "user 999", want: "session 999 not found"},
		{command: "help extra", want: "usage: help"},
	}
	for _, test := range tests {
		t.Run(test.command, func(t *testing.T) {
			var output bytes.Buffer
			console := NewConsole(hub, strings.NewReader(""), &output, ConsoleInfo{})
			console.now = func() time.Time { return now }
			if err := console.Execute(test.command); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), test.want) {
				t.Fatalf("output = %q, want substring %q", output.String(), test.want)
			}
		})
	}
}

func TestConsoleReportsAmbiguousChannelName(t *testing.T) {
	hub := voice.NewHub()
	left, err := hub.CreateChannel(domain.Channel{Name: "left"})
	if err != nil {
		t.Fatal(err)
	}
	right, err := hub.CreateChannel(domain.Channel{Name: "right"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hub.CreateChannel(domain.Channel{ParentID: left.ID, Name: "room"}); err != nil {
		t.Fatal(err)
	}
	if _, err := hub.CreateChannel(domain.Channel{ParentID: right.ID, Name: "room"}); err != nil {
		t.Fatal(err)
	}

	var output bytes.Buffer
	console := NewConsole(hub, strings.NewReader(""), &output, ConsoleInfo{})
	if err := console.Execute("channel ROOM"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "ambiguous") {
		t.Fatalf("output = %q, want ambiguity error", output.String())
	}
}

func TestConsoleRunProcessesCommandsAndEOF(t *testing.T) {
	hub := voice.NewHub()
	var output bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	console := NewConsole(
		hub,
		strings.NewReader("help\nunknown\n"),
		&output,
		ConsoleInfo{StartedAt: time.Now()},
	)
	if err := console.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "commands:") || !strings.Contains(output.String(), "unknown command") {
		t.Fatalf("unexpected output: %q", output.String())
	}
	if len(hub.ListChannels()) != 1 {
		t.Fatal("console EOF changed or stopped Hub state")
	}
	select {
	case <-ctx.Done():
		t.Fatal("console EOF canceled server context")
	default:
	}
}

func TestConsoleRunRejectsOversizedCommandAndContinues(t *testing.T) {
	exactLimit := NewConsole(
		voice.NewHub(),
		strings.NewReader(strings.Repeat("x", MaxConsoleCommandBytes)+"\n"),
		io.Discard,
		ConsoleInfo{},
	)
	if err := exactLimit.Run(context.Background()); err != nil {
		t.Fatalf("Run() rejected command at limit: %v", err)
	}

	var output bytes.Buffer
	console := NewConsole(
		voice.NewHub(),
		strings.NewReader(strings.Repeat("x", MaxConsoleCommandBytes+1)+"\nstatus\n"),
		&output,
		ConsoleInfo{StartedAt: time.Now()},
	)
	if err := console.Run(context.Background()); err != nil {
		t.Fatalf("Run() stopped after oversized command: %v", err)
	}
	if !strings.Contains(output.String(), "command exceeds 4096 bytes") ||
		!strings.Contains(output.String(), "status server=") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestConsoleRunHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	console := NewConsole(
		voice.NewHub(),
		strings.NewReader("status\n"),
		io.Discard,
		ConsoleInfo{},
	)
	if err := console.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", err)
	}
}

func TestConsoleInspectionIsRaceSafeWithHubMutations(t *testing.T) {
	hub := voice.NewHub()
	channel, err := hub.CreateChannel(domain.Channel{Name: "main"})
	if err != nil {
		t.Fatal(err)
	}
	const sessionCount = 50
	sessions := make([]voice.Session, 0, sessionCount)
	for id := uint64(1); id <= sessionCount; id++ {
		session := voice.Session{ID: id, Name: "user", LastSeen: time.Now()}
		hub.Add(&session)
		sessions = append(sessions, session)
	}
	console := NewConsole(hub, strings.NewReader(""), io.Discard, ConsoleInfo{})

	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(3)
	go func() {
		defer wait.Done()
		<-start
		for _, session := range sessions {
			if err := hub.JoinChannel(session.ID, channel.ID); err != nil && !errors.Is(err, voice.ErrSessionNotFound) {
				t.Errorf("JoinChannel() error = %v", err)
				return
			}
		}
	}()
	go func() {
		defer wait.Done()
		<-start
		for index, session := range sessions {
			if index%2 == 0 {
				hub.Remove(session.ID)
			} else {
				_ = hub.Touch(session.ID)
			}
		}
	}()
	go func() {
		defer wait.Done()
		<-start
		for index := 0; index < sessionCount; index++ {
			for _, command := range []string{"status", "channels", "users"} {
				if err := console.Execute(command); err != nil {
					t.Errorf("Execute(%q) error = %v", command, err)
					return
				}
			}
		}
	}()
	close(start)
	wait.Wait()
}

func consoleTestHub(t *testing.T) (*voice.Hub, domain.ChannelID, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 11, 12, 1, 0, 0, time.UTC)
	hub := voice.NewHub()
	main, err := hub.CreateChannel(domain.Channel{
		Name:        "main",
		Topic:       "Main topic",
		Description: "Main description",
		Position:    10,
	})
	if err != nil {
		t.Fatal(err)
	}
	hub.Add(&voice.Session{
		ID:       20,
		Name:     "alice",
		Addr:     &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9001},
		LastSeen: now.Add(-10 * time.Second),
	})
	hub.Add(&voice.Session{
		ID:       10,
		Name:     "bob",
		Addr:     &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9002},
		LastSeen: now.Add(-time.Minute),
	})
	if err := hub.JoinChannel(20, main.ID); err != nil {
		t.Fatal(err)
	}
	return hub, main.ID, now
}
