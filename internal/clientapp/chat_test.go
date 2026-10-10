package clientapp

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
	voiceclient "uniclog.io/sonoryx/internal/client"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func TestChatRequestsAreCancelledAndDrainedWithSession(t *testing.T) {
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	raw, err := net.DialUDP("udp4", nil, server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := udp.NewClientPacketConn(raw, protocol.PlainDatagramCodec{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.BindSession(9); err != nil {
		t.Fatal(err)
	}
	a := New(Options{})
	defer a.cancelLifetime()
	a.state = voiceclient.NewState(9, "alice")
	a.state.SetConnectionStatus(voiceclient.ConnectionConnected)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	session := &chatSession{ctx: ctx, cancel: cancel}
	a.chatSession = session
	a.currentConn = conn
	expectedContext := a.ChatContext() + "|0"
	done := make(chan error, 1)
	go func() {
		_, err := a.ChatRequest(context.Background(), expectedContext, protocol.ChatRequest{Operation: protocol.ChatDialogs})
		done <- err
	}()
	server.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := server.ReadFromUDP(make([]byte, 1500)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("request error %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("session cancellation did not stop request")
	}
	session.requests.Wait()
	if _, err := a.state.StartSession(10); err != nil {
		t.Fatalf("request was not drained: %v", err)
	}
	if _, err := a.ChatRequest(context.Background(), expectedContext, protocol.ChatRequest{Operation: protocol.ChatDialogs}); !errors.Is(err, ErrNotConnected) {
		t.Fatalf("cancelled session accepted request: %v", err)
	}
}
