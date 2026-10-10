package voice

import (
	"bytes"
	"net"
	"sync"
	"testing"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func TestBuildSnapshotResponsePaginatesAndDetectsRevisionChange(t *testing.T) {
	hub := NewHub()
	for i := 0; i < 40; i++ {
		mustCreateChannel(t, hub, domain.Channel{Name: "room-" + string(rune('a'+i%26)) + string(rune('A'+i/26)), Position: uint32(i)})
	}
	snapshot := hub.ClientSnapshot()
	metadata, err := buildSnapshotResponse(protocol.SnapshotRequest{Kind: protocol.SnapshotKindMetadata}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.ChannelCount != uint32(len(snapshot.Channels)) {
		t.Fatalf("channel count = %d", metadata.ChannelCount)
	}
	first, err := buildSnapshotResponse(protocol.SnapshotRequest{Kind: protocol.SnapshotKindChannels, ExpectedRevision: snapshot.Revision, Limit: 32}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if !first.HasMore || first.NextOffset == 0 || len(first.Channels) == 0 {
		t.Fatalf("first page = %+v", first)
	}
	payload, err := protocol.EncodeSnapshotResponse(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > protocol.MaxPayloadSize {
		t.Fatalf("payload size = %d", len(payload))
	}
	changed, err := buildSnapshotResponse(protocol.SnapshotRequest{Kind: protocol.SnapshotKindChannels, ExpectedRevision: snapshot.Revision - 1, Limit: 32}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Status != protocol.SnapshotStatusRevisionChanged || len(changed.Channels) != 0 {
		t.Fatalf("changed response = %+v", changed)
	}
	empty, err := buildSnapshotResponse(protocol.SnapshotRequest{Kind: protocol.SnapshotKindChannels, ExpectedRevision: snapshot.Revision, Offset: uint32(len(snapshot.Channels)), Limit: 32}, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if empty.HasMore || len(empty.Channels) != 0 {
		t.Fatalf("empty final page = %+v", empty)
	}
}

func TestHandleStateSnapshotPacketReturnsCachedDuplicate(t *testing.T) {
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
	hub := NewHub()
	session := mustCreateSession(t, hub, "alice", clientConn.LocalAddr().(*net.UDPAddr))
	if err := clientConn.BindSession(session.ID); err != nil {
		t.Fatal(err)
	}
	payload, err := protocol.EncodeSnapshotRequest(protocol.SnapshotRequest{Kind: protocol.SnapshotKindMetadata})
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.VoicePacket{Type: protocol.PacketStateSnapshotRequest, SessionID: session.ID, RequestID: 22, Payload: payload}
	cache := NewRequestCache()
	if err := HandleStateSnapshotPacket(serverConn, hub, cache, request, clientConn.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	first := receiveTestPacket(t, clientConn)
	mustCreateChannel(t, hub, domain.Channel{Name: "new"})
	if err := HandleStateSnapshotPacket(serverConn, hub, cache, request, clientConn.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	second := receiveTestPacket(t, clientConn)
	if !bytes.Equal(first.Payload, second.Payload) {
		t.Fatal("duplicate request did not return cached response")
	}
}

func TestClientSnapshotDoesNotExposeOperationalSessionData(t *testing.T) {
	hub := NewHub()
	session := mustCreateSession(t, hub, "alice", nil)
	if err := hub.JoinChannel(session.ID, DefaultChannelID); err != nil {
		t.Fatal(err)
	}
	snapshot := hub.ClientSnapshot()
	if snapshot.Info.Name != DefaultServerName || len(snapshot.Participants) != 1 || snapshot.Participants[0].DisplayName != "alice" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	snapshot.Participants[0].DisplayName = "changed"
	fresh := hub.ClientSnapshot()
	if fresh.Participants[0].DisplayName != "alice" {
		t.Fatal("client snapshot aliases hub state")
	}
}

func TestTwoClientsSnapshotJoinAndVoiceRouting(t *testing.T) {
	hub := NewHub()
	room := mustCreateChannel(t, hub, domain.Channel{Name: "room"})
	alice := mustCreateSession(t, hub, "alice", nil)
	bob := mustCreateSession(t, hub, "bob", nil)
	initial := hub.ClientSnapshot()
	if len(initial.Participants) != 2 {
		t.Fatalf("initial participants = %d", len(initial.Participants))
	}
	if err := hub.JoinChannel(alice.ID, room.ID); err != nil {
		t.Fatal(err)
	}
	if err := hub.JoinChannel(bob.ID, room.ID); err != nil {
		t.Fatal(err)
	}
	final := hub.ClientSnapshot()
	if final.Revision <= initial.Revision {
		t.Fatal("join did not advance revision")
	}
	for _, participant := range final.Participants {
		if participant.ChannelID != room.ID {
			t.Fatalf("participant %+v is outside room", participant)
		}
	}
	recipients, err := hub.RecipientsFor(alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 1 || recipients[0].ID != bob.ID {
		t.Fatalf("recipients = %+v", recipients)
	}
}

func TestClientSnapshotConcurrentWithMutations(t *testing.T) {
	hub := NewHub()
	room := mustCreateChannel(t, hub, domain.Channel{Name: "room"})
	var waitGroup sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		waitGroup.Add(1)
		go func(worker int) {
			defer waitGroup.Done()
			for index := 0; index < 100; index++ {
				session := &Session{ID: uint64(worker*1000 + index + 1), Name: "user"}
				hub.Add(session)
				_ = hub.JoinChannel(session.ID, room.ID)
				_ = hub.Touch(session.ID)
				_ = hub.ClientSnapshot()
				hub.Remove(session.ID)
			}
		}(worker)
	}
	waitGroup.Wait()
}
