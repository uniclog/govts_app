package clientcore

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	voiceclient "uniclog.io/sonoryx/internal/clientcore/internal/live"
	"uniclog.io/sonoryx/internal/identity"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/protocol/protocoltest"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func fileHTTPApp(t *testing.T, handler http.HandlerFunc) (*App, string) {
	t.Helper()
	https := httptest.NewTLSServer(handler)
	t.Cleanup(https.Close)
	endpoint := netip.MustParseAddrPort(https.Listener.Addr().String())
	socket, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { socket.Close() })
	raw, err := net.DialUDP("udp4", nil, socket.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	serverCodec, clientCodec := protocoltest.Pair(t, 9)
	server, err := udp.NewServerPacketConn(socket, serverCodec)
	if err != nil {
		t.Fatal(err)
	}
	client, err := udp.NewClientPacketConn(raw, clientCodec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	if err = client.BindSession(9); err != nil {
		t.Fatal(err)
	}
	a := newTestApp(t)
	t.Cleanup(a.cancelLifetime)
	a.state = voiceclient.NewState(9, "alice")
	a.state.SetConnectionStatus(voiceclient.ConnectionConnected)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a.chatSession = &chatSession{ctx: ctx, cancel: cancel}
	a.currentConn = client
	a.serverEndpoint = endpoint
	payload, _ := json.Marshal(protocol.FileAccess{Port: endpoint.Port(), Fingerprint: identity.TLSFingerprint(https.Certificate()), Credential: "secret"})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			packet, _, err := server.ReadPacket()
			if err != nil {
				return
			}
			a.state.CompleteRequest(voiceclient.ControlResponse{Type: protocol.PacketFileAccessAck, RequestID: packet.RequestID, Payload: payload})
		}
	}()
	t.Cleanup(func() { socket.Close(); <-done })
	return a, a.ChatContext() + "|0"
}

func TestFileHTTPBudgetWaitsUntilResponseClosedAndCanCancel(t *testing.T) {
	var calls atomic.Int32
	a, expected := fileHTTPApp(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write([]byte("ok")) })
	var closes []func()
	defer func() {
		for _, close := range closes {
			close()
		}
	}()
	for i := 0; i < 2; i++ {
		_, close, err := a.FileTransfers().Request(context.Background(), expected, "GET", "/files/v1/metadata", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		closes = append(closes, close)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, _, err := a.FileTransfers().Request(ctx, expected, "GET", "/files/v1/metadata", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiting request error: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatal("exceeded HTTP budget", calls.Load())
	}
	closes[0]()
	closes[0]() // Closing a response twice must not consume another request's slot.
	closes = closes[1:]
	_, close, err := a.FileTransfers().Request(context.Background(), expected, "GET", "/files/v1/metadata", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	close()
}

func TestFileHTTPBusyRetriesAndRewindsJSON(t *testing.T) {
	var calls atomic.Int32
	a, expected := fileHTTPApp(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if string(body) != `["1","2"]` {
			t.Errorf("request body not replayed: %q", body)
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(429)
			w.Write([]byte(`{"code":"busy"}`))
			return
		}
		w.Write([]byte(`[]`))
	})
	if _, err := a.FileTransfers().FileMetadata(context.Background(), expected, []string{"1", "2"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("busy was not retried", calls.Load())
	}
}

func TestFileHTTPBusyBackoffCanCancel(t *testing.T) {
	var calls atomic.Int32
	a, expected := fileHTTPApp(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(429)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := a.FileTransfers().FileMetadata(ctx, expected, []string{"1"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("backoff ignored cancellation: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatal("retried after cancellation", calls.Load())
	}
}
