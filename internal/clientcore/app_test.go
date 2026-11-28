package clientcore

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAppValidatesConnectAndClosesIdempotently(t *testing.T) {
	app := newTestApp(t)
	if err := app.Connect(ConnectOptions{Server: "127.0.0.1:9000"}); err == nil {
		t.Fatal("Connect accepted an empty display name")
	}
	if err := app.Connect(ConnectOptions{Name: "alice", Server: "invalid"}); err == nil {
		t.Fatal("Connect accepted an invalid server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := app.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := app.Connect(ConnectOptions{Name: "alice", Server: "127.0.0.1:9000"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Connect after Close error = %v, want ErrClosed", err)
	}
}

func TestChannelJoinErrorDoesNotRevealLevel(t *testing.T) {
	err := channelJoinError("Закрытый", errors.New("cannot join channel: channel join level is too low"))
	if err.Error() != "нет доступа к каналу \"Закрытый\"" {
		t.Fatalf("join error = %q", err)
	}
	if strings.Contains(err.Error(), "level") {
		t.Fatal("join error revealed a level")
	}
}

func TestAppConnectDisconnectAndReconnectLifecycle(t *testing.T) {
	server, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(server.LocalAddr().(*net.UDPAddr).Port))

	app := newTestApp(t)
	connect := func() {
		if err := app.Connect(ConnectOptions{Name: "alice", Server: address}); err != nil {
			t.Fatal(err)
		}
	}
	connect()
	if err := app.Connect(ConnectOptions{Name: "alice", Server: address}); !errors.Is(err, ErrAlreadyConnected) {
		t.Fatalf("duplicate Connect error = %v, want ErrAlreadyConnected", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := app.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	connect()
	if err := app.Disconnect(ctx); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

// newTestApp returns an App whose client identity and server pins live in the
// test's temporary directory instead of the user's Sonoryx profile.
func newTestApp(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	return New(Options{
		IdentityPath: filepath.Join(dir, "client.seed"),
		PinsPath:     filepath.Join(dir, "voice-pins.json"),
	})
}
