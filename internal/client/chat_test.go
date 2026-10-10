package client

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func chatTestConnection(t *testing.T) (*net.UDPConn, *udp.ClientPacketConn, *State) {
	t.Helper()
	server, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { server.Close() })
	raw, err := net.DialUDP("udp4", nil, server.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := udp.NewClientPacketConn(raw, protocol.PlainDatagramCodec{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.BindSession(9); err != nil {
		t.Fatal(err)
	}
	state := NewState(9, "alice")
	state.SetConnectionStatus(ConnectionConnected)
	return server, conn, state
}

func TestChatRequestCancellationStopsRetriesAndClearsPending(t *testing.T) {
	server, conn, state := chatTestConnection(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := RequestChat(ctx, conn, state, protocol.ChatRequest{Operation: protocol.ChatHistory, Target: domain.ChatTarget{Kind: domain.ChatChannel, ID: 1}})
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
		t.Fatal("request did not stop promptly")
	}
	if _, err := state.StartSession(10); err != nil {
		t.Fatalf("pending request survived cancellation: %v", err)
	}
	server.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err := server.ReadFromUDP(make([]byte, 1500)); err == nil {
		t.Fatal("sent a packet after cancellation")
	}
}

func TestRequestGuardRejectsRetryAfterSessionInvalidation(t *testing.T) {
	server, conn, state := chatTestConnection(t)
	generation, sessionID := state.SessionIdentity()
	done := make(chan error, 1)
	guard := func() error {
		g, id := state.SessionIdentity()
		if g != generation || id != sessionID {
			return errors.New("session changed")
		}
		return nil
	}
	go func() {
		_, err := doRequestAttempts(context.Background(), conn, state, protocol.VoicePacket{Type: protocol.PacketChatRequest}, 50*time.Millisecond, 3, guard)
		done <- err
	}()
	server.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := server.ReadFromUDP(make([]byte, 1500)); err != nil {
		t.Fatal(err)
	}
	state.InvalidateSession(ConnectionReconnecting)
	select {
	case err := <-done:
		if err == nil || err.Error() != "session changed" {
			t.Fatalf("request error %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not stop")
	}
	server.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err := server.ReadFromUDP(make([]byte, 1500)); err == nil {
		t.Fatal("sent retry for old session")
	}
	if _, err := state.StartSession(10); err != nil {
		t.Fatal(err)
	}
}

func TestCancelledChatDoesNotSendFirstPacket(t *testing.T) {
	server, conn, state := chatTestConnection(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := RequestChat(ctx, conn, state, protocol.ChatRequest{Operation: protocol.ChatDialogs})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("request error %v", err)
	}
	server.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	if _, _, err := server.ReadFromUDP(make([]byte, 1500)); err == nil {
		t.Fatal("cancelled request sent a packet")
	}
}

func TestChatRejectsAcknowledgementFromInvalidatedSession(t *testing.T) {
	server, conn, state := chatTestConnection(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := RequestChat(ctx, conn, state, protocol.ChatRequest{Operation: protocol.ChatDialogs})
		done <- err
	}()
	server.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := server.ReadFromUDP(make([]byte, 1500)); err != nil {
		t.Fatal(err)
	}
	state.InvalidateSession(ConnectionReconnecting)
	payload, err := protocol.EncodeChatPage(domain.ChatPage{UserID: 1})
	if err != nil {
		t.Fatal(err)
	}
	if !state.CompleteRequest(ControlResponse{Type: protocol.PacketChatAck, RequestID: 1, Payload: payload}) {
		t.Fatal("request was not registered")
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "сессия") {
			t.Fatalf("stale acknowledgement accepted: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("request did not complete")
	}
	if _, err := state.StartSession(10); err != nil {
		t.Fatal(err)
	}
}
