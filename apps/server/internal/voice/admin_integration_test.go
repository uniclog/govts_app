package voice_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"uniclog.io/sonoryx/apps/server/internal/persist"
	"uniclog.io/sonoryx/apps/server/internal/voice"
	"uniclog.io/sonoryx/internal/appversion"
	client "uniclog.io/sonoryx/internal/clientcore/testsupport"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

type adminClient struct {
	t      *testing.T
	conn   *udp.ClientPacketConn
	id     uint64
	result client.SecureHandshakeResult
	next   uint32
}

func (c *adminClient) request(request protocol.AdminRequest) protocol.VoicePacket {
	c.t.Helper()
	payload, err := protocol.EncodeAdminRequest(request)
	if err != nil {
		c.t.Fatal(err)
	}
	c.next++
	if err := c.conn.SendPacket(protocol.VoicePacket{Type: protocol.PacketAdminRequest, SessionID: c.id, RequestID: c.next, Payload: payload}); err != nil {
		c.t.Fatal(err)
	}
	return c.receive(func(packet protocol.VoicePacket) bool { return packet.RequestID == c.next })
}

func (c *adminClient) receive(match func(protocol.VoicePacket) bool) protocol.VoicePacket {
	c.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := c.conn.SetReadDeadline(deadline); err != nil {
			c.t.Fatal(err)
		}
		packet, err := c.conn.ReceivePacket()
		if err != nil {
			c.t.Fatalf("receive: %v", err)
		}
		if match(packet) {
			return packet
		}
	}
}

func (c *adminClient) ack(request protocol.AdminRequest) protocol.AdminResponse {
	c.t.Helper()
	packet := c.request(request)
	if packet.Type != protocol.PacketAdminAck {
		c.t.Fatalf("admin %d response = type %d %q", request.Operation, packet.Type, packet.Payload)
	}
	response, err := protocol.DecodeAdminResponse(packet.Payload)
	if err != nil {
		c.t.Fatal(err)
	}
	return response
}

func TestRemoteAdministrationOverSecureUDP(t *testing.T) {
	directory := t.TempDir()
	store, err := persist.Open(filepath.Join(directory, "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	hub := voice.NewHub()
	version, err := appversion.Parse(appversion.MinimumServerVersion)
	if err != nil {
		t.Fatal(err)
	}
	hub.SetServerVersion(version)
	if err := store.SaveInitialServer(context.Background(), persist.ServerState{Name: "server", DefaultChannelID: voice.DefaultChannelID, Channels: hub.ListChannels()}); err != nil {
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
	administration := voice.NewAdministration(store, hub)
	administration.SetPrivilegeNotifier(func(userID int64, level uint16, privileges uint8) {
		for _, session := range hub.Inspect().Sessions {
			if session.UserID == userID {
				_ = voice.SendToSession(server, session, protocol.VoicePacket{Type: protocol.PacketAccountPrivileges, SessionID: session.ID, Payload: []byte{byte(level >> 8), byte(level), privileges}})
			}
		}
	})
	authenticator.SetAdministration(administration)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = voice.ServeUDP(ctx, server, hub, voice.NewRequestCache(), authenticator) }()
	go func() { _ = voice.DispatchEvents(ctx, hub, server) }()

	endpoint := server.LocalAddr().(*net.UDPAddr).AddrPort()
	connect := func(name string, private ed25519.PrivateKey) *adminClient {
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
		t.Cleanup(func() { _ = connection.Close() })
		result, err := client.PerformSecureHandshakeAttempts(ctx, connection, codec, name, private, filepath.Join(directory, "pins.json"), endpoint.String(), time.Second, 3, appversion.MinimumServerVersion)
		if err != nil {
			t.Fatal(err)
		}
		if err := connection.BindSession(result.SessionID); err != nil {
			t.Fatal(err)
		}
		return &adminClient{t: t, conn: connection, id: result.SessionID, result: result, next: 1000}
	}
	account := func(name string) (ed25519.PrivateKey, persist.Account) {
		t.Helper()
		public, private, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		var key [32]byte
		copy(key[:], public)
		created, err := store.FindOrCreateAccount(ctx, key, name)
		if err != nil {
			t.Fatal(err)
		}
		return private, created
	}
	ownerKey, ownerAccount := account("owner")
	if err := store.SetOwner(ctx, ownerAccount.ID); err != nil {
		t.Fatal(err)
	}
	userKey, userAccount := account("user")

	owner := connect("owner", ownerKey)
	if owner.result.Permissions&protocol.PrivilegeOwner == 0 {
		t.Fatalf("owner AuthAck privileges = %08b", owner.result.Permissions)
	}
	user := connect("user", userKey)
	if user.result.Permissions != 0 {
		t.Fatalf("user AuthAck privileges = %08b", user.result.Permissions)
	}

	denied := user.request(protocol.AdminRequest{Operation: protocol.AdminAccounts})
	if denied.Type != protocol.PacketError || !strings.Contains(string(denied.Payload), "permission denied") {
		t.Fatalf("non-admin response = type %d %q", denied.Type, denied.Payload)
	}

	page := owner.ack(protocol.AdminRequest{Operation: protocol.AdminAccounts})
	if len(page.Accounts) != 2 || page.Next != 0 {
		t.Fatalf("accounts page = %+v", page)
	}
	for _, listed := range page.Accounts {
		if listed.Flags&protocol.AdminAccountOnline == 0 {
			t.Fatalf("connected account not online: %+v", listed)
		}
		if (listed.ID == uint64(ownerAccount.ID)) != (listed.Flags&protocol.AdminAccountOwner != 0) {
			t.Fatalf("owner flag on %+v", listed)
		}
	}

	granted := owner.ack(protocol.AdminRequest{Operation: protocol.AdminSetPermission, TargetID: uint64(userAccount.ID), Permission: protocol.PrivilegeAdmin, Enabled: true})
	if len(granted.Accounts) != 1 || granted.Accounts[0].Permissions != protocol.PrivilegeAdmin {
		t.Fatalf("grant response = %+v", granted)
	}
	notice := user.receive(func(packet protocol.VoicePacket) bool { return packet.Type == protocol.PacketAccountPrivileges })
	if len(notice.Payload) != 3 || notice.Payload[2] != protocol.PrivilegeAdmin {
		t.Fatalf("privileges notification = %x", notice.Payload)
	}
	if session, ok := hub.Get(user.id); !ok || session.Permissions != persist.PermissionAdmin {
		t.Fatalf("user session after grant = %+v, %t", session, ok)
	}

	// The new administrator can read but cannot touch the owner.
	if audit := user.ack(protocol.AdminRequest{Operation: protocol.AdminAudit}); len(audit.Audit) == 0 || audit.Audit[0].Action != "set-permission" || audit.Audit[0].Actor != strconv.FormatInt(ownerAccount.ID, 10) {
		t.Fatalf("audit page = %+v", audit)
	}
	refused := user.request(protocol.AdminRequest{Operation: protocol.AdminSetJoinLevel, TargetID: uint64(ownerAccount.ID), Level: 1})
	if refused.Type != protocol.PacketError || !strings.Contains(string(refused.Payload), "permission denied") {
		t.Fatalf("admin changes owner = type %d %q", refused.Type, refused.Payload)
	}

	// A retransmission is answered from the cache, not executed twice.
	payload, err := protocol.EncodeAdminRequest(protocol.AdminRequest{Operation: protocol.AdminAudit})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := owner.conn.SendPacket(protocol.VoicePacket{Type: protocol.PacketAdminRequest, SessionID: owner.id, RequestID: 77, Payload: payload}); err != nil {
			t.Fatal(err)
		}
		if packet := owner.receive(func(packet protocol.VoicePacket) bool { return packet.RequestID == 77 }); packet.Type != protocol.PacketAdminAck {
			t.Fatalf("cached response = type %d %q", packet.Type, packet.Payload)
		}
	}

	// Channel changes reach other clients as ChannelUpdated events.
	channelEvent := func(id uint64) domain.Channel {
		t.Helper()
		packet := user.receive(func(packet protocol.VoicePacket) bool {
			if packet.Type != protocol.PacketStateEvent {
				return false
			}
			event, err := protocol.DecodeStateEvent(packet.Payload)
			return err == nil && event.Kind == domain.ChannelUpdated && uint64(event.Channel.ID) == id
		})
		event, _ := protocol.DecodeStateEvent(packet.Payload)
		return event.Channel
	}
	room := protocol.AdminChannel{Name: "room", Topic: "talk", MaxUsers: 5, Bitrate: 48_000, Application: uint8(domain.OpusApplicationAudio)}
	created := owner.ack(protocol.AdminRequest{Operation: protocol.AdminCreateChannel, Channel: room})
	if created.Next == 0 {
		t.Fatalf("create channel response = %+v", created)
	}
	if got := channelEvent(created.Next); got.Name != "room" || got.MaxUsers != 5 || got.Audio.Bitrate != 48_000 || got.Audio.Application != domain.OpusApplicationAudio {
		t.Fatalf("created channel event = %+v", got)
	}
	room.Name, room.ParentID, room.Position = "moved room", uint64(voice.DefaultChannelID), 3
	owner.ack(protocol.AdminRequest{Operation: protocol.AdminUpdateChannel, TargetID: created.Next, Channel: room})
	if got := channelEvent(created.Next); got.Name != "moved room" || got.ParentID != voice.DefaultChannelID || got.Position != 3 {
		t.Fatalf("updated channel event = %+v", got)
	}
	if stored, ok := hub.GetChannel(domain.ChannelID(created.Next)); !ok || stored.Name != "moved room" {
		t.Fatalf("hub channel = %+v, %t", stored, ok)
	}

	// Deleting a channel moves its members to the default channel first.
	stateEvent := func(match func(domain.StateEvent) bool) domain.StateEvent {
		t.Helper()
		packet := user.receive(func(packet protocol.VoicePacket) bool {
			if packet.Type != protocol.PacketStateEvent {
				return false
			}
			event, err := protocol.DecodeStateEvent(packet.Payload)
			return err == nil && match(event)
		})
		event, _ := protocol.DecodeStateEvent(packet.Payload)
		return event
	}
	if err := hub.JoinChannel(user.id, domain.ChannelID(created.Next)); err != nil {
		t.Fatal(err)
	}
	if deleted := owner.ack(protocol.AdminRequest{Operation: protocol.AdminDeleteChannel, TargetID: created.Next}); deleted.Next != created.Next {
		t.Fatalf("delete channel response = %+v", deleted)
	}
	moved := stateEvent(func(event domain.StateEvent) bool {
		return event.Kind == domain.ParticipantMoved && event.SessionID == user.id && event.ChannelID == voice.DefaultChannelID
	})
	removed := stateEvent(func(event domain.StateEvent) bool { return event.Kind == domain.ChannelDeleted })
	if removed.ChannelID != domain.ChannelID(created.Next) || removed.Revision != moved.Revision+1 {
		t.Fatalf("deleted event = %+v after move %+v", removed, moved)
	}
	if session, ok := hub.Get(user.id); !ok || session.ChannelID != voice.DefaultChannelID {
		t.Fatalf("member after delete = %+v, %t", session, ok)
	}
	if refused := owner.request(protocol.AdminRequest{Operation: protocol.AdminDeleteChannel, TargetID: uint64(voice.DefaultChannelID)}); refused.Type != protocol.PacketError {
		t.Fatalf("default channel delete = type %d %q", refused.Type, refused.Payload)
	}

	banned := owner.ack(protocol.AdminRequest{Operation: protocol.AdminSetBan, TargetID: uint64(userAccount.ID), Enabled: true, Reason: "test"})
	if len(banned.Accounts) != 1 || banned.Accounts[0].Flags&protocol.AdminAccountBanned == 0 || banned.Accounts[0].BanReason != "test" {
		t.Fatalf("ban response = %+v", banned)
	}
	if _, ok := hub.Get(user.id); ok {
		t.Fatal("banned session survived")
	}

	// A deleted account is disconnected; its key registers a new account.
	guestKey, guestAccount := account("guest")
	guest := connect("guest", guestKey)
	if deleted := owner.ack(protocol.AdminRequest{Operation: protocol.AdminDeleteAccount, TargetID: uint64(guestAccount.ID)}); deleted.Next != uint64(guestAccount.ID) || len(deleted.Accounts) != 0 {
		t.Fatalf("delete account response = %+v", deleted)
	}
	if _, ok := hub.Get(guest.id); ok {
		t.Fatal("deleted account's session survived")
	}
	returned := connect("guest", guestKey)
	if session, ok := hub.Get(returned.id); !ok || session.UserID == guestAccount.ID || session.Permissions != 0 {
		t.Fatalf("returning session = %+v, %t", session, ok)
	}
	for _, listed := range owner.ack(protocol.AdminRequest{Operation: protocol.AdminAccounts}).Accounts {
		if listed.ID == uint64(guestAccount.ID) {
			t.Fatalf("deleted account listed: %+v", listed)
		}
	}
	if refused := owner.request(protocol.AdminRequest{Operation: protocol.AdminDeleteAccount, TargetID: uint64(userAccount.ID)}); refused.Type != protocol.PacketError {
		t.Fatalf("banned account delete = type %d %q", refused.Type, refused.Payload)
	}

	// The per-session budget counts new requests only.
	var limited protocol.VoicePacket
	for range 40 {
		limited = owner.request(protocol.AdminRequest{Operation: protocol.AdminAccounts})
		if limited.Type == protocol.PacketError {
			break
		}
	}
	if limited.Type != protocol.PacketError || !strings.Contains(string(limited.Payload), "too many admin requests") {
		t.Fatalf("rate limit response = type %d %q", limited.Type, limited.Payload)
	}
}
