package voice

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/appversion"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func TestValidateSessionAddr(t *testing.T) {
	hub := NewHub()

	originalAddr := &net.UDPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: 5000,
	}

	session := mustCreateSession(t, hub, "alice", originalAddr)

	t.Run("same address", func(t *testing.T) {
		addr := &net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: 5000,
		}

		err := ValidateSessionAddr(hub, session.ID, addr)
		if err != nil {
			t.Fatalf("expected valid address, got %v", err)
		}
	})

	t.Run("different port", func(t *testing.T) {
		addr := &net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: 6000,
		}

		err := ValidateSessionAddr(hub, session.ID, addr)
		if err == nil {
			t.Fatal("expected address mismatch")
		}
	})

	t.Run("unknown session", func(t *testing.T) {
		err := ValidateSessionAddr(
			hub,
			999,
			originalAddr,
		)

		if err == nil {
			t.Fatal("expected unknown session error")
		}
	})
}

func TestHandleHeartbeatPacketAcknowledgesAndRejectsSessions(t *testing.T) {
	serverRaw, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	serverConn := mustPacketConn(t, serverRaw)
	defer serverConn.Close()
	clientRaw, err := net.DialUDP("udp4", nil, serverRaw.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	clientConn := mustClientPacketConn(t, clientRaw)
	defer clientConn.Close()
	hub := NewHub()
	addr := clientConn.LocalAddr().(*net.UDPAddr)
	session := mustCreateSession(t, hub, "alice", addr)
	if err := clientConn.BindSession(session.ID); err != nil {
		t.Fatal(err)
	}
	cache := NewRequestCache()
	request := protocol.VoicePacket{Type: protocol.PacketHeartbeat, SessionID: session.ID, RequestID: 7}
	if err := HandleHeartbeatPacket(serverConn, hub, cache, request, addr); err != nil {
		t.Fatal(err)
	}
	response := receiveTestPacket(t, clientConn)
	if response.Type != protocol.PacketHeartbeatAck || response.RequestID != request.RequestID || len(response.Payload) != 0 {
		t.Fatalf("heartbeat response = %+v", response)
	}
	if response.Sequence != 0 {
		t.Fatalf("empty voice feedback = %x, want 0%%", response.Sequence)
	}
	hub.RecordVoicePacket(session.ID, 1)
	hub.RecordVoicePacket(session.ID, 4)
	hub.RecordVoicePacket(session.ID, 2)
	hub.RecordVoicePacket(session.ID, 2) // duplicate must not inflate received count
	request.RequestID++
	request.Sequence = 4<<14 | 4
	if err := HandleHeartbeatPacket(serverConn, hub, cache, request, addr); err != nil {
		t.Fatal(err)
	}
	response = receiveTestPacket(t, clientConn)
	if response.Sequence != 2500 {
		t.Fatalf("voice feedback = %x, want 25%% loss", response.Sequence)
	}
	request.SessionID++
	request.RequestID++
	if err := HandleHeartbeatPacket(serverConn, hub, cache, request, addr); err != nil {
		t.Fatal(err)
	}
	response = receiveTestPacket(t, clientConn)
	if response.Type != protocol.PacketSessionInvalid || response.RequestID != request.RequestID {
		t.Fatalf("invalid-session response = %+v", response)
	}

	otherRaw, err := net.DialUDP("udp4", nil, serverRaw.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	otherConn := mustClientPacketConn(t, otherRaw)
	defer otherConn.Close()
	request.SessionID = session.ID
	request.RequestID++
	otherAddr := otherConn.LocalAddr().(*net.UDPAddr)
	if err := HandleHeartbeatPacket(serverConn, hub, cache, request, otherAddr); err != nil {
		t.Fatal(err)
	}
	response = receiveTestPacket(t, otherConn)
	if response.Type != protocol.PacketSessionInvalid || response.RequestID != request.RequestID {
		t.Fatalf("wrong-endpoint response = %+v", response)
	}
}

func TestVoiceReceivedWindowExpiresOldPackets(t *testing.T) {
	hub := NewHub()
	session := mustCreateSession(t, hub, "alice", nil)
	now := time.Now()
	hub.recordVoicePacketAt(session.ID, 1, now.Add(-31*time.Second))
	hub.recordVoicePacketAt(session.ID, 2, now.Add(-29*time.Second))
	hub.recordVoicePacketAt(session.ID, 3, now)
	hub.recordVoicePacketAt(session.ID, 3, now) // duplicate
	if got := hub.VoiceReceivedWindow(session.ID, now, 3, 2); got != 2 {
		t.Fatalf("received in client sequence window = %d, want 2", got)
	}
	if got := hub.VoiceReceivedWindow(session.ID, now.Add(36*time.Second), 3, 3); got != 0 {
		t.Fatalf("expired window = %d", got)
	}
}

func TestHandlePacketSilentlyDropsVoiceForUnknownSession(t *testing.T) {
	err := HandlePacket(nil, NewHub(), NewRequestCache(), protocol.VoicePacket{
		Type:      protocol.PacketVoice,
		SessionID: 42,
	}, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5000})
	if err != nil {
		t.Fatalf("HandlePacket() error = %v", err)
	}
}

func TestFindRecipientsRejectsSenderWithoutChannel(t *testing.T) {
	hub := NewHub()
	sender := mustCreateSession(t, hub, "alice", nil)

	_, err := FindRecipients(hub, protocol.VoicePacket{
		Type:      protocol.PacketVoice,
		SessionID: sender.ID,
	})
	if !errors.Is(err, ErrSessionNotInChannel) {
		t.Fatalf(
			"FindRecipients() error = %v, want %v",
			err,
			ErrSessionNotInChannel,
		)
	}
}

func TestFindRecipientsReturnsOnlySameChannel(t *testing.T) {
	hub := NewHub()
	sender := mustCreateSession(t, hub, "alice", nil)
	sameChannel := mustCreateSession(t, hub, "bob", nil)
	otherChannel := mustCreateSession(t, hub, "carol", nil)
	music := mustCreateChannel(t, hub, domain.Channel{Name: "music"})
	gaming := mustCreateChannel(t, hub, domain.Channel{Name: "gaming"})

	if err := hub.JoinChannel(sender.ID, music.ID); err != nil {
		t.Fatal(err)
	}
	if err := hub.JoinChannel(sameChannel.ID, music.ID); err != nil {
		t.Fatal(err)
	}
	if err := hub.JoinChannel(otherChannel.ID, gaming.ID); err != nil {
		t.Fatal(err)
	}

	recipients, err := FindRecipients(hub, protocol.VoicePacket{
		Type:      protocol.PacketVoice,
		SessionID: sender.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 1 {
		t.Fatalf("FindRecipients() returned %d sessions, want 1", len(recipients))
	}
	if recipients[0].ID != sameChannel.ID {
		t.Fatalf(
			"FindRecipients() returned session %d, want %d",
			recipients[0].ID,
			sameChannel.ID,
		)
	}
}

type contextRecordingCodec struct {
	encodedContext protocol.DatagramContext
	encodedPacket  protocol.VoicePacket
}

func (c *contextRecordingCodec) Encode(
	ctx protocol.DatagramContext,
	packet protocol.VoicePacket,
) ([]byte, error) {
	c.encodedContext = ctx
	c.encodedPacket = packet
	return (protocol.PlainDatagramCodec{}).Encode(ctx, packet)
}

func (c *contextRecordingCodec) Decode(
	ctx protocol.DatagramContext,
	datagram []byte,
) (protocol.VoicePacket, error) {
	return (protocol.PlainDatagramCodec{}).Decode(ctx, datagram)
}

func TestSendToSessionUsesRecipientAsKeyOwner(t *testing.T) {
	serverRaw, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	clientRaw, err := net.DialUDP("udp4", nil, serverRaw.LocalAddr().(*net.UDPAddr))
	if err != nil {
		_ = serverRaw.Close()
		t.Fatal(err)
	}

	codec := &contextRecordingCodec{}
	serverConn, err := udp.NewServerPacketConn(serverRaw, codec)
	if err != nil {
		_ = serverRaw.Close()
		_ = clientRaw.Close()
		t.Fatal(err)
	}
	clientConn := mustClientPacketConn(t, clientRaw)
	t.Cleanup(func() {
		_ = serverConn.Close()
		_ = clientConn.Close()
	})

	const (
		senderID    = 41
		recipientID = 73
	)
	packet := protocol.VoicePacket{
		Type:      protocol.PacketVoice,
		SessionID: senderID,
		Sequence:  9,
		Payload:   []byte("voice"),
	}
	recipient := Session{
		ID:   recipientID,
		Addr: clientConn.LocalAddr().(*net.UDPAddr),
	}
	if err := SendToSession(serverConn, recipient, packet); err != nil {
		t.Fatal(err)
	}

	if codec.encodedContext.Direction != protocol.DirectionServerToClient {
		t.Fatalf("direction = %d, want server to client", codec.encodedContext.Direction)
	}
	if codec.encodedContext.KeyOwnerID != recipientID {
		t.Fatalf(
			"key owner = %d, want recipient %d",
			codec.encodedContext.KeyOwnerID,
			recipientID,
		)
	}
	if codec.encodedPacket.SessionID != senderID {
		t.Fatalf(
			"packet session ID = %d, want sender %d",
			codec.encodedPacket.SessionID,
			senderID,
		)
	}
}

func TestHandleJoinChannelPacketReturnsCachedResponseForDuplicate(t *testing.T) {
	serverConn, err := net.ListenUDP("udp4", &net.UDPAddr{
		IP: net.ParseIP("127.0.0.1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	serverPacketConn := mustPacketConn(t, serverConn)
	defer serverPacketConn.Close()

	clientConn, err := net.DialUDP(
		"udp4",
		nil,
		serverConn.LocalAddr().(*net.UDPAddr),
	)
	if err != nil {
		t.Fatal(err)
	}
	clientPacketConn := mustClientPacketConn(t, clientConn)
	defer clientPacketConn.Close()

	clientAddr := clientPacketConn.LocalAddr().(*net.UDPAddr)
	hub := NewHub()
	music := mustCreateChannel(t, hub, domain.Channel{Name: "music"})
	session := mustCreateSession(t, hub, "alice", clientAddr)
	if err := clientPacketConn.BindSession(session.ID); err != nil {
		t.Fatal(err)
	}
	cache := NewRequestCache()
	joinPayload, err := protocol.EncodeJoinChannelRequest(music.ID)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.VoicePacket{
		Type:      protocol.PacketJoinChannel,
		SessionID: session.ID,
		RequestID: 7,
		Payload:   joinPayload,
	}

	if err := HandleJoinChannelPacket(
		serverPacketConn,
		hub,
		cache,
		request,
		clientAddr,
	); err != nil {
		t.Fatal(err)
	}
	firstResponse := receiveTestPacket(t, clientPacketConn)

	request.Payload, err = protocol.EncodeJoinChannelRequest(DefaultChannelID)
	if err != nil {
		t.Fatal(err)
	}
	if err := HandleJoinChannelPacket(
		serverPacketConn,
		hub,
		cache,
		request,
		clientAddr,
	); err != nil {
		t.Fatal(err)
	}
	secondResponse := receiveTestPacket(t, clientPacketConn)

	updatedSession, ok := hub.Get(session.ID)
	if !ok {
		t.Fatalf("session %d not found", session.ID)
	}
	if got := updatedSession.ChannelID; got != music.ID {
		t.Fatalf("session channel = %d, want original channel %d", got, music.ID)
	}
	firstID, _, err := protocol.DecodeJoinChannelAck(firstResponse.Payload)
	if err != nil || firstID != music.ID {
		t.Fatalf("first response = (%d, %v), want channel %d", firstID, err, music.ID)
	}
	if !bytes.Equal(secondResponse.Payload, firstResponse.Payload) {
		t.Fatal("cached response payload differs")
	}
}

func TestHandleJoinChannelPacketRejectsUnknownAndFullChannels(t *testing.T) {
	serverConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	serverPacketConn := mustPacketConn(t, serverConn)
	defer serverPacketConn.Close()

	clientConn, err := net.DialUDP("udp4", nil, serverConn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	clientPacketConn := mustClientPacketConn(t, clientConn)
	defer clientPacketConn.Close()

	clientAddr := clientPacketConn.LocalAddr().(*net.UDPAddr)
	hub := NewHub()
	full := mustCreateChannel(t, hub, domain.Channel{Name: "full", MaxUsers: 1})
	occupant := mustCreateSession(t, hub, "occupant", nil)
	if err := hub.JoinChannel(occupant.ID, full.ID); err != nil {
		t.Fatal(err)
	}
	session := mustCreateSession(t, hub, "alice", clientAddr)
	if err := clientPacketConn.BindSession(session.ID); err != nil {
		t.Fatal(err)
	}
	cache := NewRequestCache()
	missingPayload, err := protocol.EncodeJoinChannelRequest(999)
	if err != nil {
		t.Fatal(err)
	}

	request := protocol.VoicePacket{
		Type:      protocol.PacketJoinChannel,
		SessionID: session.ID,
		RequestID: 8,
		Payload:   missingPayload,
	}
	if err := HandleJoinChannelPacket(serverPacketConn, hub, cache, request, clientAddr); err != nil {
		t.Fatal(err)
	}
	unknownResponse := receiveTestPacket(t, clientPacketConn)
	if unknownResponse.Type != protocol.PacketError ||
		!strings.Contains(string(unknownResponse.Payload), ErrChannelNotFound.Error()) {
		t.Fatalf("unknown channel response = %+v", unknownResponse)
	}
	if len(hub.ListChannels()) != 2 {
		t.Fatal("unknown join created a channel")
	}

	request.RequestID++
	request.Payload, err = protocol.EncodeJoinChannelRequest(full.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := HandleJoinChannelPacket(serverPacketConn, hub, cache, request, clientAddr); err != nil {
		t.Fatal(err)
	}
	fullResponse := receiveTestPacket(t, clientPacketConn)
	if fullResponse.Type != protocol.PacketError ||
		!strings.Contains(string(fullResponse.Payload), ErrChannelFull.Error()) {
		t.Fatalf("full channel response = %+v", fullResponse)
	}

	restricted := mustCreateChannel(t, hub, domain.Channel{Name: "restricted", MinJoinLevel: 25})
	request.RequestID++
	request.Payload, err = protocol.EncodeJoinChannelRequest(restricted.ID)
	if err != nil {
		t.Fatal(err)
	}
	var logOutput bytes.Buffer
	previousOutput := log.Writer()
	log.SetOutput(&logOutput)
	defer log.SetOutput(previousOutput)
	if err := HandleJoinChannelPacket(serverPacketConn, hub, cache, request, clientAddr); err != nil {
		t.Fatal(err)
	}
	deniedResponse := receiveTestPacket(t, clientPacketConn)
	if deniedResponse.Type != protocol.PacketError || !strings.Contains(string(deniedResponse.Payload), ErrChannelForbidden.Error()) {
		t.Fatalf("restricted channel response = %+v", deniedResponse)
	}
	if err := HandleJoinChannelPacket(serverPacketConn, hub, cache, request, clientAddr); err != nil {
		t.Fatal(err)
	}
	_ = receiveTestPacket(t, clientPacketConn)
	entry := logOutput.String()
	if strings.Count(entry, "channel join rejected:") != 1 ||
		!strings.Contains(entry, "channel_id="+strconv.FormatUint(uint64(restricted.ID), 10)) ||
		!strings.Contains(entry, "reason=permission_denied") ||
		strings.Contains(entry, "join_level") {
		t.Fatalf("unexpected denial log: %q", entry)
	}

	stored, ok := hub.Get(session.ID)
	if !ok {
		t.Fatal("joining session disappeared")
	}
	if stored.ChannelID != 0 {
		t.Fatalf("failed joins moved session to channel %d", stored.ChannelID)
	}
}

func TestHandleHelloPacketReturnsSameSessionForDuplicate(t *testing.T) {
	serverConn, err := net.ListenUDP("udp4", &net.UDPAddr{
		IP: net.ParseIP("127.0.0.1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	serverPacketConn := mustPacketConn(t, serverConn)
	defer serverPacketConn.Close()

	clientConn, err := net.DialUDP(
		"udp4",
		nil,
		serverConn.LocalAddr().(*net.UDPAddr),
	)
	if err != nil {
		t.Fatal(err)
	}
	clientPacketConn := mustClientPacketConn(t, clientConn)
	defer clientPacketConn.Close()

	clientAddr := clientPacketConn.LocalAddr().(*net.UDPAddr)
	hub := NewHub()
	cache := NewRequestCache()
	hello := protocol.VoicePacket{
		Type:      protocol.PacketHello,
		RequestID: 17,
		Payload:   []byte("alice"),
	}

	if err := HandleHelloPacket(serverPacketConn, hub, cache, hello, clientAddr); err != nil {
		t.Fatal(err)
	}
	firstResponse := receiveTestPacket(t, clientPacketConn)

	if err := HandleHelloPacket(serverPacketConn, hub, cache, hello, clientAddr); err != nil {
		t.Fatal(err)
	}
	secondResponse := receiveTestPacket(t, clientPacketConn)

	if hub.Count() != 1 {
		t.Fatalf("Hub.Count() = %d, want 1", hub.Count())
	}
	if firstResponse.SessionID == 0 {
		t.Fatal("first response returned zero session ID")
	}
	if secondResponse.SessionID != firstResponse.SessionID {
		t.Fatalf(
			"duplicate hello returned session %d, want %d",
			secondResponse.SessionID,
			firstResponse.SessionID,
		)
	}
	if secondResponse.RequestID != hello.RequestID {
		t.Fatalf(
			"duplicate hello response RequestID = %d, want %d",
			secondResponse.RequestID,
			hello.RequestID,
		)
	}
}

func TestHelloRejectsMinimumAboveServerVersion(t *testing.T) {
	serverRaw, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	serverConn := mustPacketConn(t, serverRaw)
	defer serverConn.Close()
	clientRaw, err := net.DialUDP("udp4", nil, serverRaw.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	clientConn := mustClientPacketConn(t, clientRaw)
	defer clientConn.Close()
	hub := NewHub()
	minimum, _ := appversion.Parse("0.2.0")
	request := protocol.VoicePacket{Type: protocol.PacketHello, RequestID: 19, Sequence: uint32(minimum), Payload: []byte("alice")}
	if err := HandleHelloPacket(serverConn, hub, NewRequestCache(), request, clientConn.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	response := receiveTestPacket(t, clientConn)
	if response.Type != protocol.PacketServerVersionTooOld || response.Sequence != uint32(hub.ServerVersion()) || hub.Count() != 0 {
		t.Fatalf("version rejection = %+v, sessions=%d", response, hub.Count())
	}
}

func receiveTestPacket(t *testing.T, conn *udp.ClientPacketConn) protocol.VoicePacket {
	t.Helper()

	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	packet, err := conn.ReceivePacket()
	if err != nil {
		t.Fatal(err)
	}
	return packet
}

func mustClientPacketConn(t *testing.T, conn *net.UDPConn) *udp.ClientPacketConn {
	t.Helper()

	packetConn, err := udp.NewClientPacketConn(conn, protocol.PlainDatagramCodec{})
	if err != nil {
		t.Fatalf("NewClientPacketConn() error = %v", err)
	}
	return packetConn
}

func mustPacketConn(t *testing.T, conn *net.UDPConn) *udp.ServerPacketConn {
	t.Helper()

	packetConn, err := udp.NewServerPacketConn(conn, protocol.PlainDatagramCodec{})
	if err != nil {
		t.Fatalf("NewServerPacketConn() error = %v", err)
	}
	return packetConn
}

func TestServeUDPStopsOnContextCancellation(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{
		IP: net.ParseIP("127.0.0.1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	packetConn := mustPacketConn(t, conn)
	defer packetConn.Close()

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		resultCh <- ServeUDP(ctx, packetConn, NewHub(), NewRequestCache())
	}()

	cancel()
	select {
	case err := <-resultCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ServeUDP() error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		t.Fatal("ServeUDP() did not stop after context cancellation")
	}
}

func TestServeUDPReturnsClosedConnectionError(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{
		IP: net.ParseIP("127.0.0.1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	packetConn := mustPacketConn(t, conn)
	if err := packetConn.Close(); err != nil {
		t.Fatal(err)
	}

	err = ServeUDP(context.Background(), packetConn, NewHub(), NewRequestCache())
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("ServeUDP() error = %v, want %v", err, net.ErrClosed)
	}
}

func TestServeUDPSkipsMalformedPacket(t *testing.T) {
	serverConn, err := net.ListenUDP("udp4", &net.UDPAddr{
		IP: net.ParseIP("127.0.0.1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	serverPacketConn := mustPacketConn(t, serverConn)
	defer serverPacketConn.Close()

	clientConn, err := net.DialUDP(
		"udp4",
		nil,
		serverConn.LocalAddr().(*net.UDPAddr),
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	resultCh := make(chan error, 1)
	go func() {
		resultCh <- ServeUDP(ctx, serverPacketConn, NewHub(), NewRequestCache())
	}()

	if _, err := clientConn.Write([]byte{protocol.PacketHello}); err != nil {
		t.Fatal(err)
	}
	clientPacketConn := mustClientPacketConn(t, clientConn)
	defer clientPacketConn.Close()
	if err := clientPacketConn.SendPacket(protocol.VoicePacket{
		Type:      protocol.PacketHello,
		RequestID: 17,
		Payload:   []byte("alice"),
	}); err != nil {
		t.Fatal(err)
	}

	response := receiveTestPacket(t, clientPacketConn)
	if response.Type != protocol.PacketHelloAck {
		t.Fatalf("response type = %d, want %d", response.Type, protocol.PacketHelloAck)
	}

	cancel()
	select {
	case err := <-resultCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ServeUDP() error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		t.Fatal("ServeUDP() did not stop")
	}
}
