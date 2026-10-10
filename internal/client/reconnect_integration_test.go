package client

import (
	"bytes"
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/audio"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
	"uniclog.io/sonoryx/internal/voice"
)

func TestClientRecoversAfterServerHubRestart(t *testing.T) {
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

	firstHub := voice.NewHub()
	stopFirst := serveTestHub(t, serverConn, firstHub)
	firstID, err := PerformHandshakeAttempt(context.Background(), clientConn, "alice", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := clientConn.BindSession(firstID); err != nil {
		t.Fatal(err)
	}
	state := NewState(0, "alice")
	if _, err := state.StartSession(firstID); err != nil {
		t.Fatal(err)
	}
	stopClient := startTestControlPipeline(t, clientConn, state)
	if err := heartbeatProbe(context.Background(), clientConn, state, time.Second); err != nil {
		t.Fatal(err)
	}
	stopFirst()

	secondHub := voice.NewHub()
	stopSecond := serveTestHub(t, serverConn, secondHub)
	defer stopSecond()
	if err := heartbeatProbe(context.Background(), clientConn, state, time.Second); !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("old heartbeat error = %v", err)
	}
	stopClient()
	state.InvalidateSession(ConnectionReconnecting)
	if err := clientConn.ClearSession(firstID); err != nil {
		t.Fatal(err)
	}
	secondID, err := PerformHandshakeAttempt(context.Background(), clientConn, "alice", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if secondID == firstID {
		t.Fatal("server restart reused session ID")
	}
	if err := clientConn.BindSession(secondID); err != nil {
		t.Fatal(err)
	}
	if _, err := state.StartSession(secondID); err != nil {
		t.Fatal(err)
	}
	stopClient = startTestControlPipeline(t, clientConn, state)
	defer stopClient()
	snapshot, err := LoadServerSnapshot(context.Background(), clientConn, state)
	if err != nil {
		t.Fatal(err)
	}
	channelID, err := ResolveChannel(snapshot, voice.DefaultChannelName)
	if err != nil {
		t.Fatal(err)
	}
	if err := JoinChannel(context.Background(), clientConn, state, channelID); err != nil {
		t.Fatal(err)
	}
	final, err := LoadServerSnapshot(context.Background(), clientConn, state)
	if err != nil {
		t.Fatal(err)
	}
	if len(final.Participants) != 1 || final.Participants[0].SessionID != secondID || final.Participants[0].ChannelID != channelID {
		t.Fatalf("final snapshot = %+v", final)
	}

	rawReceiver, err := net.DialUDP("udp4", nil, rawServer.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	receiverConn, err := udp.NewClientPacketConn(rawReceiver, protocol.PlainDatagramCodec{})
	if err != nil {
		t.Fatal(err)
	}
	defer receiverConn.Close()
	receiver, err := secondHub.CreateSession("bob", receiverConn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	if err := secondHub.JoinChannel(receiver.ID, channelID); err != nil {
		t.Fatal(err)
	}
	if err := receiverConn.BindSession(receiver.ID); err != nil {
		t.Fatal(err)
	}
	wantVoice := []byte{1, 2, 3, 4}
	if err := clientConn.SendPacket(protocol.VoicePacket{Type: protocol.PacketVoice, SessionID: secondID, Sequence: 9, Payload: wantVoice}); err != nil {
		t.Fatal(err)
	}
	if err := receiverConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	voicePacket, err := receiverConn.ReceivePacket()
	if err != nil {
		t.Fatal(err)
	}
	if voicePacket.Type != protocol.PacketVoice || voicePacket.SessionID != secondID || voicePacket.Sequence != 9 || !bytes.Equal(voicePacket.Payload, wantVoice) {
		t.Fatalf("voice packet after reconnect = %+v", voicePacket)
	}
}

func TestHandshakeCanRetryWhenClientStartsBeforeServer(t *testing.T) {
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

	if _, err := PerformHandshakeAttempt(context.Background(), clientConn, "alice", 20*time.Millisecond); err == nil {
		t.Fatal("handshake succeeded before server started")
	}
	stopServer := serveTestHub(t, serverConn, voice.NewHub())
	defer stopServer()
	if sessionID, err := PerformHandshakeAttempt(context.Background(), clientConn, "alice", time.Second); err != nil || sessionID == 0 {
		t.Fatalf("handshake after server start = (%d, %v)", sessionID, err)
	}
}

func serveTestHub(t *testing.T, conn *udp.ServerPacketConn, hub *voice.Hub) func() {
	t.Helper()
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- voice.ServeUDP(ctx, conn, hub, voice.NewRequestCache()) }()
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
func startTestControlPipeline(t *testing.T, conn *udp.ClientPacketConn, state *State) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	encoded := make(chan audio.MediaFrame)
	control := make(chan protocol.VoicePacket, 8)
	done := make(chan struct{}, 2)
	go func() { _ = ReceiveLoop(ctx, conn, encoded, control); done <- struct{}{} }()
	go func() { _ = ControlLoop(ctx, state, control); done <- struct{}{} }()
	return func() {
		cancel()
		for range 2 {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("client control pipeline did not stop")
			}
		}
	}
}
