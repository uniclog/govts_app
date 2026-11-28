package client

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	voice "uniclog.io/sonoryx/apps/server/testsupport/voice"
	"uniclog.io/sonoryx/internal/audio"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func TestClientRecoversAfterServerHubRestart(t *testing.T) {
	server := newSecureTestServer(t)
	alice := newSecureTestClient(t, server.addr())
	clientConn := alice.conn

	firstHub := voice.NewHub()
	stopFirst := server.serve(t, firstHub)
	firstID := alice.connect(t, context.Background(), "alice")
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
	stopSecond := server.serve(t, secondHub)
	defer stopSecond()
	if err := heartbeatProbe(context.Background(), clientConn, state, time.Second); !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("old heartbeat error = %v", err)
	}
	stopClient()
	state.InvalidateSession(ConnectionReconnecting)
	if err := clientConn.ClearSession(firstID); err != nil {
		t.Fatal(err)
	}
	alice.codec.Remove(firstID)
	secondID := alice.connect(t, context.Background(), "alice")
	if secondID == firstID {
		t.Fatal("server restart reused session ID")
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

	bob := newSecureTestClient(t, server.addr())
	receiverConn := bob.conn
	receiverID := bob.connect(t, context.Background(), "bob")
	if err := secondHub.JoinChannel(receiverID, channelID); err != nil {
		t.Fatal(err)
	}
	wantVoice := []byte{1, 2, 3, 4}
	if err := secondHub.RequestVoiceState(receiverID, time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, delivery := range secondHub.DueVoiceStates(time.Now()) {
		page, err := protocol.DecodeVoiceState(delivery.Packets[0].Payload)
		if err != nil || !secondHub.AckVoiceState(delivery.Session.ID, page.Revision) {
			t.Fatal("voice state ack failed", err)
		}
	}
	source, _ := secondHub.Get(secondID)
	outgoing, err := protocol.NewVoiceBundleV2Packet(secondID, protocol.VoiceBundleV2{DatagramSequence: 1, SourceEpoch: source.SourceEpoch, Frames: []protocol.VoiceBundleFrame{{Sequence: 9, Payload: wantVoice}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := clientConn.SendPacket(outgoing); err != nil {
		t.Fatal(err)
	}

	if err := receiverConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	voicePacket, err := receiverConn.ReceivePacket()
	if err != nil {
		t.Fatal(err)
	}
	bundle, decodeErr := protocol.DecodeVoiceBundleV2Packet(voicePacket, false)
	if decodeErr != nil || voicePacket.SessionID != secondID || voicePacket.Sequence != 9 || len(bundle.Frames) != 1 || !bytes.Equal(bundle.Frames[0].Payload, wantVoice) {
		t.Fatalf("voice packet after reconnect = %+v", voicePacket)
	}
}

func TestHandshakeCanRetryWhenClientStartsBeforeServer(t *testing.T) {
	server := newSecureTestServer(t)
	alice := newSecureTestClient(t, server.addr())

	if _, err := alice.handshake(context.Background(), "alice", 20*time.Millisecond, 1); err == nil {
		t.Fatal("handshake succeeded before server started")
	}
	stopServer := server.serve(t, voice.NewHub())
	defer stopServer()
	if sessionID, err := alice.handshake(context.Background(), "alice", time.Second, 1); err != nil || sessionID == 0 {
		t.Fatalf("handshake after server start = (%d, %v)", sessionID, err)
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
