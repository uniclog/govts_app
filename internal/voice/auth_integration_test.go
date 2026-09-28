package voice_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"uniclog.io/govts/internal/appversion"
	"uniclog.io/govts/internal/client"
	"uniclog.io/govts/internal/domain"
	"uniclog.io/govts/internal/persist"
	"uniclog.io/govts/internal/protocol"
	"uniclog.io/govts/internal/transport/udp"
	"uniclog.io/govts/internal/voice"
)

func TestSecureAuthenticationAndJoinLevel(t *testing.T) {
	directory := t.TempDir()
	store, err := persist.Open(filepath.Join(directory, "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hub := voice.NewHub()
	version, err := appversion.Parse("0.2.5")
	if err != nil {
		t.Fatal(err)
	}
	hub.SetServerVersion(version)
	privateRoom, err := hub.CreateChannel(domain.Channel{Name: "private", MinJoinLevel: 25})
	if err != nil {
		t.Fatal(err)
	}
	_, serverKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverCodec := protocol.NewSecureDatagramCodec(true)
	authenticator, err := voice.NewAuthenticator(serverKey, store, serverCodec)
	if err != nil {
		t.Fatal(err)
	}
	rawServer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	server, err := udp.NewServerPacketConn(rawServer, serverCodec)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	go func() { serverDone <- voice.ServeUDP(ctx, server, hub, voice.NewRequestCache(), authenticator) }()

	publicKey, clientKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	copy(key[:], publicKey)
	account, err := store.FindOrCreateAccount(ctx, key, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetJoinLevel(ctx, account.ID, 25); err != nil {
		t.Fatal(err)
	}
	for _, permission := range []uint8{persist.PermissionKick, persist.PermissionBan, persist.PermissionDrag} {
		if _, err := store.SetPermission(ctx, account.ID, permission, true); err != nil {
			t.Fatal(err)
		}
	}

	endpoint := server.LocalAddr().(*net.UDPAddr).AddrPort()
	legacyRaw, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(endpoint))
	if err != nil {
		t.Fatal(err)
	}
	legacyCodec := protocol.NewSecureDatagramCodec(false)
	legacyConn, err := udp.NewClientPacketConn(legacyRaw, legacyCodec)
	if err != nil {
		t.Fatal(err)
	}
	defer legacyConn.Close()
	_, err = client.PerformSecureHandshakeAttempts(ctx, legacyConn, legacyCodec, "alice", clientKey, filepath.Join(directory, "pins.json"), endpoint.String(), time.Second, 1, "0.2.0")
	var legacyRejected *client.AuthenticationRejectedError
	if !errors.As(err, &legacyRejected) || !strings.Contains(legacyRejected.Reason, "upgrade") {
		t.Fatalf("legacy client error = %v", err)
	}
	openClient := func(private ed25519.PrivateKey) (*udp.ClientPacketConn, uint64) {
		t.Helper()
		raw, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(endpoint))
		if err != nil {
			t.Fatal(err)
		}
		codec := protocol.NewSecureDatagramCodec(false)
		connection, err := udp.NewClientPacketConn(raw, codec)
		if err != nil {
			t.Fatal(err)
		}
		result, err := client.PerformSecureHandshakeAttempts(ctx, connection, codec, "alice", private, filepath.Join(directory, "pins.json"), endpoint.String(), time.Second, 3, "0.2.5")
		if err != nil {
			t.Fatal(err)
		}
		if err := connection.BindSession(result.SessionID); err != nil {
			t.Fatal(err)
		}
		return connection, result.SessionID
	}

	first, firstID := openClient(clientKey)
	defer first.Close()
	if session, ok := hub.Get(firstID); !ok || session.UserID != account.ID || session.JoinLevel != 25 {
		t.Fatalf("authenticated session = %+v, found=%t", session, ok)
	}
	joinPayload, err := protocol.EncodeJoinChannelRequest(privateRoom.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.SendPacket(protocol.VoicePacket{Type: protocol.PacketJoinChannel, SessionID: firstID, RequestID: 100, Payload: joinPayload}); err != nil {
		t.Fatal(err)
	}
	if err := first.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	response, err := first.ReceivePacket()
	if err != nil || response.Type != protocol.PacketJoinChannelAck {
		t.Fatalf("join response = %+v, %v", response, err)
	}

	_, otherKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	second, secondID := openClient(otherKey)
	defer second.Close()
	if err := second.SendPacket(protocol.VoicePacket{Type: protocol.PacketJoinChannel, SessionID: secondID, RequestID: 101, Payload: joinPayload}); err != nil {
		t.Fatal(err)
	}
	if err := second.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	response, err = second.ReceivePacket()
	if err != nil || response.Type != protocol.PacketError || !strings.Contains(string(response.Payload), "join level") {
		t.Fatalf("denied join response = %+v, %v", response, err)
	}
	if err := second.SendPacket(protocol.VoicePacket{Type: protocol.PacketKick, SessionID: secondID, RequestID: 107, Payload: binary.BigEndian.AppendUint64(nil, firstID)}); err != nil {
		t.Fatal(err)
	}
	if err := second.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	response, err = second.ReceivePacket()
	if err != nil || response.Type != protocol.PacketError || !strings.Contains(string(response.Payload), "permission denied") {
		t.Fatalf("denied kick response = %+v, %v", response, err)
	}
	defaultPayload, err := protocol.EncodeJoinChannelRequest(voice.DefaultChannelID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		connection *udp.ClientPacketConn
		id         uint64
		request    uint32
	}{{first, firstID, 102}, {second, secondID, 103}} {
		if err := item.connection.SendPacket(protocol.VoicePacket{Type: protocol.PacketJoinChannel, SessionID: item.id, RequestID: item.request, Payload: defaultPayload}); err != nil {
			t.Fatal(err)
		}
		if err := item.connection.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		response, err := item.connection.ReceivePacket()
		if err != nil || response.Type != protocol.PacketJoinChannelAck {
			t.Fatalf("default join response = %+v, %v", response, err)
		}
	}
	if err := first.SendPacket(protocol.VoicePacket{Type: protocol.PacketVoice, SessionID: firstID, Sequence: 1, Payload: []byte{1, 2, 3}}); err != nil {
		t.Fatal(err)
	}
	if err := second.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	response, err = second.ReceivePacket()
	if err != nil || response.Type != protocol.PacketVoice || response.SessionID != firstID || string(response.Payload) != string([]byte{1, 2, 3}) {
		t.Fatalf("relayed secure voice = %+v, %v", response, err)
	}
	moderate := func(action uint8, target uint64, requestID uint32) {
		t.Helper()
		payload := binary.BigEndian.AppendUint64(nil, target)
		if err := first.SendPacket(protocol.VoicePacket{Type: action, SessionID: firstID, RequestID: requestID, Payload: payload}); err != nil {
			t.Fatal(err)
		}
		if err := first.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		response, err := first.ReceivePacket()
		if err != nil || response.Type != protocol.PacketModerationAck {
			t.Fatalf("moderation response = %+v, %v", response, err)
		}
	}
	moderate(protocol.PacketKick, secondID, 104)
	if _, ok := hub.Get(secondID); ok {
		t.Fatal("kicked session survived")
	}
	reconnected, reconnectedID := openClient(otherKey)
	defer reconnected.Close()
	dragPayload := binary.BigEndian.AppendUint64(nil, reconnectedID)
	dragPayload = binary.BigEndian.AppendUint64(dragPayload, uint64(privateRoom.ID))
	if err := first.SendPacket(protocol.VoicePacket{Type: protocol.PacketDrag, SessionID: firstID, RequestID: 105, Payload: dragPayload}); err != nil {
		t.Fatal(err)
	}
	if err := first.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	response, err = first.ReceivePacket()
	if err != nil || response.Type != protocol.PacketError || !strings.Contains(string(response.Payload), "join level") {
		t.Fatalf("forbidden drag response = %+v, %v", response, err)
	}
	auditDB, err := sql.Open("sqlite3", filepath.Join(directory, "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer auditDB.Close()
	if _, err := auditDB.Exec(`CREATE TRIGGER fail_drag BEFORE INSERT ON audit WHEN NEW.action='drag' BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	sendDrag := func(requestID uint32) protocol.VoicePacket {
		t.Helper()
		payload := binary.BigEndian.AppendUint64(nil, reconnectedID)
		payload = binary.BigEndian.AppendUint64(payload, uint64(voice.DefaultChannelID))
		if err := first.SendPacket(protocol.VoicePacket{Type: protocol.PacketDrag, SessionID: firstID, RequestID: requestID, Payload: payload}); err != nil {
			t.Fatal(err)
		}
		if err := first.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		response, err := first.ReceivePacket()
		if err != nil {
			t.Fatal(err)
		}
		return response
	}
	response = sendDrag(108)
	if response.Type != protocol.PacketError {
		t.Fatalf("drag with failed audit response = %+v", response)
	}
	if session, ok := hub.Get(reconnectedID); !ok || session.ChannelID != 0 {
		t.Fatalf("target moved despite failed audit: %+v, found=%t", session, ok)
	}
	if _, err := auditDB.Exec(`DROP TRIGGER fail_drag`); err != nil {
		t.Fatal(err)
	}
	response = sendDrag(109)
	if response.Type != protocol.PacketModerationAck {
		t.Fatalf("valid drag response = %+v", response)
	}
	if session, ok := hub.Get(reconnectedID); !ok || session.ChannelID != voice.DefaultChannelID {
		t.Fatalf("valid drag target = %+v, found=%t", session, ok)
	}
	moderate(protocol.PacketBan, reconnectedID, 106)
	if _, ok := hub.Get(reconnectedID); ok {
		t.Fatal("banned session survived")
	}
	rawBanned, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(endpoint))
	if err != nil {
		t.Fatal(err)
	}
	bannedCodec := protocol.NewSecureDatagramCodec(false)
	bannedConn, err := udp.NewClientPacketConn(rawBanned, bannedCodec)
	if err != nil {
		t.Fatal(err)
	}
	defer bannedConn.Close()
	_, err = client.PerformSecureHandshakeAttempts(ctx, bannedConn, bannedCodec, "alice", otherKey, filepath.Join(directory, "pins.json"), endpoint.String(), time.Second, 3, "0.2.5")
	var rejected *client.AuthenticationRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("banned reconnect error = %v", err)
	}
	cancel()
	if err := <-serverDone; err != nil && err != context.Canceled {
		t.Fatal(err)
	}
}
