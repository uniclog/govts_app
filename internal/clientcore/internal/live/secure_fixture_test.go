package client

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"path/filepath"
	"testing"
	"time"

	persist "uniclog.io/sonoryx/apps/server/testsupport/persist"
	voice "uniclog.io/sonoryx/apps/server/testsupport/voice"
	"uniclog.io/sonoryx/internal/appversion"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

// secureTestServer is a loopback voice server. Its identity key and account
// store outlive individual hubs, as they do across a real server restart.
type secureTestServer struct {
	conn  *udp.ServerPacketConn
	codec *protocol.SecureDatagramCodec
	key   ed25519.PrivateKey
	store *persist.Store
}

func newSecureTestServer(t *testing.T) *secureTestServer {
	t.Helper()
	raw, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	codec := protocol.NewSecureDatagramCodec(true)
	conn, err := udp.NewServerPacketConn(raw, codec)
	if err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	store, err := persist.Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &secureTestServer{conn: conn, codec: codec, key: key, store: store}
}

func (s *secureTestServer) addr() *net.UDPAddr {
	return s.conn.LocalAddr().(*net.UDPAddr)
}

// serve runs voice.ServeUDP for hub until the returned function is called.
func (s *secureTestServer) serve(t *testing.T, hub *voice.Hub) func() {
	t.Helper()
	version, err := appversion.Parse(appversion.ServerVersion)
	if err != nil {
		t.Fatal(err)
	}
	hub.SetServerVersion(version)
	authenticator, err := voice.NewAuthenticator(s.key, s.store, s.codec)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- voice.ServeUDP(ctx, s.conn, hub, voice.NewRequestCache(), authenticator) }()
	return func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("ServeUDP() error = %v", err)
			}
		case <-time.After(time.Second):
			t.Error("ServeUDP() did not stop")
		}
	}
}

// secureTestClient holds a client connection and the identity it
// authenticates with. Its server pins live in the test's temporary directory.
type secureTestClient struct {
	conn  *udp.ClientPacketConn
	codec *protocol.SecureDatagramCodec
	key   ed25519.PrivateKey
	pins  string
}

func newSecureTestClient(t *testing.T, server *net.UDPAddr) *secureTestClient {
	t.Helper()
	raw, err := net.DialUDP("udp4", nil, server)
	if err != nil {
		t.Fatal(err)
	}
	codec := protocol.NewSecureDatagramCodec(false)
	conn, err := udp.NewClientPacketConn(raw, codec)
	if err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return &secureTestClient{conn: conn, codec: codec, key: key, pins: filepath.Join(t.TempDir(), "voice-pins.json")}
}

// handshake authenticates as name and returns the new session ID. It does
// not bind the session to the connection.
func (c *secureTestClient) handshake(ctx context.Context, name string, timeout time.Duration, attempts int) (uint64, error) {
	result, err := PerformSecureHandshakeAttempts(ctx, c.conn, c.codec, name, c.key, c.pins, c.conn.RemoteAddr().String(), timeout, attempts, appversion.MinimumServerVersion)
	return result.SessionID, err
}

// connect authenticates as name and binds the session to the connection.
func (c *secureTestClient) connect(t *testing.T, ctx context.Context, name string) uint64 {
	t.Helper()
	id, err := c.handshake(ctx, name, time.Second, requestAttempts)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.conn.BindSession(id); err != nil {
		t.Fatal(err)
	}
	return id
}
