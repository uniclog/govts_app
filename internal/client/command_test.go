package client

import (
	"bytes"
	"context"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
	"uniclog.io/sonoryx/internal/voice"
)

func TestReadCommandLoopParsesCommands(t *testing.T) {
	commands := make(chan Command, 3)
	ReadCommandLoop(context.Background(), strings.NewReader("/HELP\n/join gaming\n/channels\n"), commands)
	want := []Command{{Name: "/help"}, {Name: "/join", Arguments: []string{"gaming"}}, {Name: "/channels"}}
	for _, expected := range want {
		got := <-commands
		if got.Name != expected.Name || strings.Join(got.Arguments, " ") != strings.Join(expected.Arguments, " ") {
			t.Fatalf("command = %+v, want %+v", got, expected)
		}
	}
}

func TestReadCommandLoopStopsWhenCommandDeliveryIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		ReadCommandLoop(ctx, strings.NewReader("/channels\n"), make(chan Command))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("command reader remained blocked after cancellation")
	}
}

func TestWriteClientHelpListsCommands(t *testing.T) {
	var output bytes.Buffer
	if err := WriteClientHelp(&output); err != nil {
		t.Fatal(err)
	}
	for _, command := range []string{"/channels", "/join <id|name>", "/rnnoise", "/vad", "/vad-mode", "/vad-sensitivity", "/help", "/quit"} {
		if !strings.Contains(output.String(), command) {
			t.Fatalf("help missing %q", command)
		}
	}
}

func TestHandleChannelsRefreshesAndPreservesStaleSnapshot(t *testing.T) {
	rawServer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	serverConn, err := udp.NewServerPacketConn(rawServer, protocol.PlainDatagramCodec{})
	if err != nil {
		t.Fatal(err)
	}
	defer serverConn.Close()
	rawClient, err := net.DialUDP("udp4", nil, rawServer.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	clientConn, err := udp.NewClientPacketConn(rawClient, protocol.PlainDatagramCodec{})
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()
	stopServer := serveTestHub(t, serverConn, voice.NewHub())
	sessionID, err := PerformHandshakeAttempt(context.Background(), clientConn, "alice", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := clientConn.BindSession(sessionID); err != nil {
		t.Fatal(err)
	}
	state := NewState(0, "alice")
	if _, err := state.StartSession(sessionID); err != nil {
		t.Fatal(err)
	}
	stopClient := startTestControlPipeline(t, clientConn, state)
	defer stopClient()

	var fresh bytes.Buffer
	handleChannels(context.Background(), clientConn, state, &fresh)
	if !state.SnapshotFresh() || !strings.Contains(fresh.String(), voice.DefaultServerName) || strings.Contains(fresh.String(), "[stale]") {
		t.Fatalf("fresh channels output = %q", fresh.String())
	}
	previous := state.Snapshot()
	stopServer()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	var stale bytes.Buffer
	handleChannels(canceled, clientConn, state, &stale)
	if !reflect.DeepEqual(state.Snapshot(), previous) {
		t.Fatal("failed refresh replaced the previous snapshot")
	}
	if !strings.Contains(stale.String(), "[stale]") {
		t.Fatalf("stale channels output = %q", stale.String())
	}
}

func TestOneCommandReaderSurvivesOfflineAndSessionScopes(t *testing.T) {
	commands := make(chan Command, 3)
	ReadCommandLoop(context.Background(), strings.NewReader("/channels\n/help\n/quit\n"), commands)
	appCtx, cancelApp := context.WithCancel(context.Background())
	defer cancelApp()
	HandleOfflineCommand(<-commands, &bytes.Buffer{}, cancelApp)
	if appCtx.Err() != nil {
		t.Fatal("network command canceled the application while offline")
	}
	var output bytes.Buffer
	if err := SessionCommandLoop(context.Background(), nil, nil, commands, &output, cancelApp, nil); err != nil {
		t.Fatal(err)
	}
	if appCtx.Err() == nil {
		t.Fatal("/quit did not cancel the application")
	}
	if !strings.Contains(output.String(), "/channels") {
		t.Fatalf("session did not process /help: %q", output.String())
	}
}
