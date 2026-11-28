package clientcore

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/appversion"
	voiceclient "uniclog.io/sonoryx/internal/clientcore/internal/live"
	"uniclog.io/sonoryx/internal/protocol"
)

func TestReconnectDelayStaysWithinSpread(t *testing.T) {
	seen := make(map[time.Duration]bool)
	for range 1000 {
		delay := reconnectDelay(4 * time.Second)
		if delay < 3200*time.Millisecond || delay > 4800*time.Millisecond {
			t.Fatalf("delay = %s", delay)
		}
		seen[delay] = true
	}
	if len(seen) < 2 {
		t.Fatal("delays have no spread")
	}
}

func TestPlainNetworkErrorMeansConnectionLost(t *testing.T) {
	// The form in which a wsasend failure left voice_telemetry.
	failure := errors.New("write udp: wsasend: no buffer space available")
	err := classifyNetworkLoopError("voice telemetry", failure)
	if !errors.Is(err, voiceclient.ErrConnectionLost) || !errors.Is(err, failure) {
		t.Fatalf("classified error = %v", err)
	}
}

func listenTestServer(t *testing.T) (*net.UDPConn, netip.AddrPort) {
	t.Helper()
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server, server.LocalAddr().(*net.UDPAddr).AddrPort()
}

func TestFirstConnectionReportsServerRefusalAtOnce(t *testing.T) {
	server, endpoint := listenTestServer(t)
	go func() {
		buffer := make([]byte, protocol.MaxWireDatagramSize+1)
		n, addr, err := server.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		request, err := protocol.DecodePacket(buffer[:n])
		if err != nil {
			return
		}
		reply, err := protocol.EncodePacket(protocol.NewErrorPacket(0, request.RequestID, "server is full"))
		if err != nil {
			return
		}
		_, _ = server.WriteToUDP(reply, addr)
	}()
	app := newTestApp(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := app.runConnection(ctx, endpoint, "alice", appversion.MinimumServerVersion)
	var rejected *voiceclient.AuthenticationRejectedError
	if !errors.As(err, &rejected) || !rejected.Temporary() {
		t.Fatalf("connection error = %v", err)
	}
}

func TestSilentServerGetsANewSocketAfterFailedHandshakes(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for three real handshake timeouts")
	}
	t.Parallel()
	server, endpoint := listenTestServer(t)
	var mu sync.Mutex
	ports := make(map[int]bool)
	replaced := make(chan struct{})
	go func() {
		buffer := make([]byte, protocol.MaxWireDatagramSize+1)
		for {
			_, addr, err := server.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			mu.Lock()
			ports[addr.Port] = true
			if len(ports) == 2 {
				close(replaced)
			}
			mu.Unlock()
		}
	}()
	app := newTestApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- app.runConnection(ctx, endpoint, "alice", appversion.MinimumServerVersion) }()
	select {
	case <-replaced:
	case err := <-done:
		t.Fatalf("connection stopped retrying: %v", err)
	case <-time.After(30 * time.Second):
		t.Fatal("handshakes never moved to a new local port")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("connection did not stop")
	}
}
