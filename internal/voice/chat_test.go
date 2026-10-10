package voice

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/persist"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func TestFitChatPagePreservesCursorAndDatagramBudget(t *testing.T) {
	for _, forward := range []bool{false, true} {
		p := domain.ChatPage{UserID: 1}
		for i := 0; i < 17; i++ {
			id := int64(17 - i)
			if forward {
				id = int64(i + 1)
			}
			p.Messages = append(p.Messages, domain.ChatMessage{ID: id, ChannelID: 1, SenderID: 1, ClientID: [16]byte{byte(id)}, SentAtMS: 1, SenderName: "alice", Text: strings.Repeat("a", 1000)})
		}
		b, err := fitChatPage(&p, protocol.ChatRequest{Operation: protocol.ChatHistory, Forward: forward})
		if err != nil || len(b) > protocol.MaxPayloadSize || len(p.Messages) != 1 || !p.HasMore || p.Cursor != p.Messages[0].ID {
			t.Fatalf("fit forward=%t: %+v, %v", forward, p, err)
		}
		if _, err := protocol.DecodeChatPage(b); err != nil {
			t.Fatal(err)
		}
	}
}

func TestChatCommittedSendIsAcknowledgedAfterChannelChange(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	path := filepath.Join(t.TempDir(), "server.db")
	s, err := persist.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	h := NewHub()
	if err := s.SaveInitialServer(ctx, persist.ServerState{Name: "server", DefaultChannelID: h.ClientSnapshot().Info.DefaultChannelID, Channels: h.ListChannels()}); err != nil {
		t.Fatal(err)
	}
	account, err := s.FindOrCreateAccount(ctx, [32]byte{1}, "alice")
	if err != nil {
		t.Fatal(err)
	}
	other, err := h.CreateChannel(domain.Channel{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	rawServer, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	server, err := udp.NewServerPacketConn(rawServer, protocol.PlainDatagramCodec{})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	rawClient, err := net.DialUDP("udp4", nil, rawServer.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	client, err := udp.NewClientPacketConn(rawClient, protocol.PlainDatagramCodec{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	session, _, err := h.CreateAuthenticatedSession("alice", rawClient.LocalAddr().(*net.UDPAddr), account.ID, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	channel := h.ListChannels()[0].ID
	if err := h.JoinChannel(session.ID, channel); err != nil {
		t.Fatal(err)
	}
	if err := client.BindSession(session.ID); err != nil {
		t.Fatal(err)
	}

	w := newChatWorker(&Authenticator{store: s}, server, h)
	request := protocol.ChatRequest{Operation: protocol.ChatSend, Target: domain.ChatTarget{Kind: domain.ChatChannel, ID: int64(channel)}, ClientID: [16]byte{1}, Text: "accepted before moving"}
	job := chatJob{packet: protocol.VoicePacket{SessionID: session.ID, RequestID: 1}, addr: rawClient.LocalAddr().(*net.UDPAddr), user: account.ID, request: request}
	actor, err := w.check(job)
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.SaveChat(ctx, actor.UserID, actor.Name, request.Target, request.ClientID, request.Text)
	if err != nil {
		t.Fatal(err)
	}
	// Reproduce the exact phase boundary without timing-dependent SQL locks:
	// authorization and commit succeeded, then membership changed before delivery.
	if err := h.JoinChannel(session.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	w.complete(ctx, job, actor, domain.ChatPage{UserID: actor.UserID, LatestID: m.ID, Cursor: m.ID, Messages: []domain.ChatMessage{m}}, nil)
	client.SetReadDeadline(time.Now().Add(time.Second))
	ack, err := client.ReceivePacket()
	if err != nil || ack.Type != protocol.PacketChatAck {
		t.Fatalf("committed send response: type=%d payload=%q err=%v", ack.Type, ack.Payload, err)
	}
	p, err := protocol.DecodeChatPage(ack.Payload)
	if err != nil || len(p.Messages) != 1 || p.Messages[0].Text != request.Text {
		t.Fatalf("ack %+v, %v", p, err)
	}
	// The same change must still prevent disclosure of channel history.
	job.request = protocol.ChatRequest{Operation: protocol.ChatHistory, Target: request.Target}
	job.packet.RequestID = 2
	w.complete(ctx, job, actor, p, nil)
	response, err := client.ReceivePacket()
	if err != nil || response.Type != protocol.PacketError {
		t.Fatalf("history after move: type=%d err=%v", response.Type, err)
	}
}

func TestChatAuthorizationRestrictsChannelAndSelfDialog(t *testing.T) {
	h := NewHub()
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 10000}
	session, _, err := h.CreateAuthenticatedSession("alice", addr, 1, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	channel := h.ListChannels()[0].ID
	w := &chatWorker{hub: h}
	job := chatJob{packet: protocol.VoicePacket{SessionID: session.ID}, addr: addr, user: 1, request: protocol.ChatRequest{Operation: protocol.ChatHistory, Target: domain.ChatTarget{Kind: domain.ChatChannel, ID: int64(channel)}}}
	if _, err := w.check(job); err == nil {
		t.Fatal("non-member allowed to read channel")
	}
	if err := h.JoinChannel(session.ID, channel); err != nil {
		t.Fatal(err)
	}
	if _, err := w.check(job); err != nil {
		t.Fatal(err)
	}
	job.request.Target = domain.ChatTarget{Kind: domain.ChatDirect, ID: 1}
	if _, err := w.check(job); err == nil {
		t.Fatal("self dialog allowed")
	}
}
