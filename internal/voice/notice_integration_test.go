package voice_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/appversion"
	"uniclog.io/sonoryx/internal/client"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/identity"
	"uniclog.io/sonoryx/internal/persist"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
	"uniclog.io/sonoryx/internal/voice"
)

type noticeServer struct {
	t        *testing.T
	ctx      context.Context
	hub      *voice.Hub
	endpoint netip.AddrPort
	pins     string
}

func startNoticeServer(t *testing.T) *noticeServer {
	t.Helper()
	directory := t.TempDir()
	store, err := persist.Open(filepath.Join(directory, "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	hub := voice.NewHub()
	version, err := appversion.Parse(appversion.MinimumServerVersion)
	if err != nil {
		t.Fatal(err)
	}
	hub.SetServerVersion(version)
	// Channel chat rows reference the persisted channel tree.
	for _, channel := range hub.ListChannels() {
		if err := store.InsertChannelBy(context.Background(), "test", channel); err != nil {
			t.Fatal(err)
		}
	}
	_, serverKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	codec := protocol.NewSecureDatagramCodec(true)
	authenticator, err := voice.NewAuthenticator(serverKey, store, codec)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	server, err := udp.NewServerPacketConn(raw, codec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- voice.ServeUDP(ctx, server, hub, voice.NewRequestCache(), authenticator) }()
	t.Cleanup(func() { cancel(); _ = server.Close(); <-done })
	return &noticeServer{t: t, ctx: ctx, hub: hub, endpoint: server.LocalAddr().(*net.UDPAddr).AddrPort(), pins: filepath.Join(directory, "pins.json")}
}

func (s *noticeServer) dial() (*udp.ClientPacketConn, *protocol.SecureDatagramCodec) {
	s.t.Helper()
	raw, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(s.endpoint))
	if err != nil {
		s.t.Fatal(err)
	}
	codec := protocol.NewSecureDatagramCodec(false)
	conn, err := udp.NewClientPacketConn(raw, codec)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { _ = conn.Close() })
	return conn, codec
}

func (s *noticeServer) participant(private ed25519.PrivateKey, name string) (*udp.ClientPacketConn, uint64) {
	s.t.Helper()
	conn, codec := s.dial()
	result, err := client.PerformSecureHandshakeAttempts(s.ctx, conn, codec, name, private, s.pins, s.endpoint.String(), time.Second, 3, appversion.MinimumServerVersion)
	if err != nil {
		s.t.Fatal(err)
	}
	if err := conn.BindSession(result.SessionID); err != nil {
		s.t.Fatal(err)
	}
	return conn, result.SessionID
}

func (s *noticeServer) watcher(private ed25519.PrivateKey, pins string) (*udp.ClientPacketConn, uint64, error) {
	s.t.Helper()
	conn, codec := s.dial()
	result, err := client.PerformWatchHandshake(s.ctx, conn, codec, "watcher", private, pins, s.endpoint.String(), time.Second, 3, appversion.MinimumServerVersion, "")
	if err != nil {
		return nil, 0, err
	}
	if err := conn.BindSession(result.SessionID); err != nil {
		s.t.Fatal(err)
	}
	return conn, result.SessionID, nil
}

// receive reads until a packet of the wanted type arrives; others, such as
// state events, are skipped.
func receive(t *testing.T, conn *udp.ClientPacketConn, want uint8) protocol.VoicePacket {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	if err := conn.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	for {
		packet, err := conn.ReceivePacket()
		if errors.Is(err, protocol.ErrRejectedDatagram) {
			continue
		}
		if err != nil {
			t.Fatalf("waiting for packet type %d: %v", want, err)
		}
		if packet.Type == want {
			return packet
		}
	}
}

func newKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return private
}

func TestWatcherReceivesArrivalAndMessageNotices(t *testing.T) {
	server := startNoticeServer(t)
	alice, bob, stranger := newKey(t), newKey(t), newKey(t)

	// Nobody can confirm a key for a watcher: without a pin it does not connect.
	if _, _, err := server.watcher(alice, filepath.Join(t.TempDir(), "empty-pins.json")); !errors.Is(err, identity.ErrServerNotPinned) {
		t.Fatalf("watcher without a pin: %v, want ErrServerNotPinned", err)
	}
	// Joining once pins the key and registers the account; then alice leaves.
	aliceMain, aliceMainID := server.participant(alice, "alice")
	if err := client.Disconnect(aliceMain, aliceMainID); err != nil {
		t.Fatal(err)
	}
	// A watcher never creates an account.
	_, _, err := server.watcher(stranger, server.pins)
	var rejected *client.AuthenticationRejectedError
	if !errors.As(err, &rejected) || rejected.Reason != voice.ErrWatcherNeedsAccount.Error() {
		t.Fatalf("watcher without an account: %v", err)
	}

	watch, watchID, err := server.watcher(alice, server.pins)
	if err != nil {
		t.Fatal(err)
	}
	if !server.hub.Known(watchID) {
		t.Fatal("the server does not know the watcher")
	}
	if _, ok := server.hub.Get(watchID); ok {
		t.Fatal("the watcher is a participant")
	}
	if err := watch.SendPacket(protocol.VoicePacket{Type: protocol.PacketHeartbeat, SessionID: watchID, RequestID: 1}); err != nil {
		t.Fatal(err)
	}
	if ack := receive(t, watch, protocol.PacketHeartbeatAck); ack.RequestID != 1 {
		t.Fatalf("heartbeat ack = %+v", ack)
	}
	// Anything but a heartbeat would treat the watcher as a participant.
	join, err := protocol.EncodeJoinChannelRequest(voice.DefaultChannelID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := watch.SendPacket(protocol.VoicePacket{Type: protocol.PacketJoinChannel, SessionID: watchID, RequestID: 2, Payload: join}); err != nil {
		t.Fatal(err)
	}
	if reply := receive(t, watch, protocol.PacketError); reply.RequestID != 2 {
		t.Fatalf("join from a watcher answered with %+v", reply)
	}

	// Bob arrives and is online: the watcher hears about it.
	bobConn, bobID := server.participant(bob, "bob")
	if err := bobConn.SendPacket(protocol.VoicePacket{Type: protocol.PacketNoticePrefs, SessionID: bobID, RequestID: 10, Payload: protocol.EncodeNoticePrefs(protocol.NoticeAnnounce)}); err != nil {
		t.Fatal(err)
	}
	if ack := receive(t, bobConn, protocol.PacketNoticePrefsAck); len(ack.Payload) != 1 || ack.Payload[0] != protocol.NoticeAnnounce {
		t.Fatalf("notice prefs ack = %+v", ack)
	}
	bobSession, ok := server.hub.Get(bobID)
	if !ok {
		t.Fatal("bob has no session")
	}
	arrival, err := protocol.DecodeNotice(receive(t, watch, protocol.PacketNotice).Payload)
	if err != nil {
		t.Fatal(err)
	}
	if arrival.Kind != protocol.NoticeArrival || arrival.UserID != bobSession.UserID || arrival.SessionID != bobID || arrival.Name != "bob" {
		t.Fatalf("arrival notice = %+v", arrival)
	}

	// Bob writes in the default channel: the watcher gets the excerpt.
	if err := bobConn.SendPacket(protocol.VoicePacket{Type: protocol.PacketJoinChannel, SessionID: bobID, RequestID: 11, Payload: join}); err != nil {
		t.Fatal(err)
	}
	receive(t, bobConn, protocol.PacketJoinChannelAck)
	send, err := protocol.EncodeChatRequest(protocol.ChatRequest{Operation: protocol.ChatSend, Target: domain.ChatTarget{Kind: domain.ChatChannel, ID: int64(voice.DefaultChannelID)}, ClientID: [16]byte{1}, Text: "hello\nthere"})
	if err != nil {
		t.Fatal(err)
	}
	if err := bobConn.SendPacket(protocol.VoicePacket{Type: protocol.PacketChatRequest, SessionID: bobID, RequestID: 12, Payload: send}); err != nil {
		t.Fatal(err)
	}
	receive(t, bobConn, protocol.PacketChatAck)
	message, err := protocol.DecodeNotice(receive(t, watch, protocol.PacketNotice).Payload)
	if err != nil {
		t.Fatal(err)
	}
	if message.Kind != protocol.NoticeMessage || message.UserID != bobSession.UserID || message.Name != "bob" ||
		message.Target != (domain.ChatTarget{Kind: domain.ChatChannel, ID: int64(voice.DefaultChannelID)}) ||
		message.ChannelName != voice.DefaultChannelName || message.Text != "hello there" || message.MessageID <= 0 {
		t.Fatalf("message notice = %+v", message)
	}

	// A clean exit removes the session at once, even when copies arrive.
	if err := client.Disconnect(bobConn, bobID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { _, ok := server.hub.Get(bobID); return !ok })
	if err := client.Disconnect(watch, watchID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, func() bool { return !server.hub.Known(watchID) })
}

func waitUntil(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
