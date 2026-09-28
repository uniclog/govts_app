package voice

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"uniclog.io/govts/internal/domain"
	"uniclog.io/govts/internal/protocol"
)

func TestHubEventRevisionsAndReplacement(t *testing.T) {
	h := NewHub()
	if _, err := h.CreateChannel(domain.Channel{Name: "main"}); err != nil {
		t.Fatal(err)
	}
	if len(h.takeEvents()) != 0 {
		t.Fatal("bootstrap generated history")
	}
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234}
	one := mustCreateSession(t, h, "one", addr)
	if err := h.JoinChannel(one.ID, DefaultChannelID); err != nil {
		t.Fatal(err)
	}
	rev := h.Revision()
	if err := h.JoinChannel(one.ID, DefaultChannelID); err != nil {
		t.Fatal(err)
	}
	if h.Revision() != rev {
		t.Fatal("idempotent join changed revision")
	}
	two, _, err := h.CreateSessionReplacingEndpoint("two", addr)
	if err != nil {
		t.Fatal(err)
	}
	h.RemoveInactive(time.Now().Add(time.Minute), 30*time.Second)
	events := h.takeEvents()
	want := []domain.StateEventKind{domain.ParticipantJoined, domain.ParticipantMoved, domain.ParticipantLeft, domain.ParticipantJoined, domain.ParticipantLeft}
	if len(events) != len(want) {
		t.Fatalf("events=%+v", events)
	}
	for i, e := range events {
		if e.Kind != want[i] || e.Revision != domain.StateRevision(i+3) {
			t.Fatalf("event %d = %+v", i, e)
		}
		if err := e.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	if events[2].SessionID != one.ID || events[4].SessionID != two.ID {
		t.Fatal("incorrect removed IDs")
	}
}

func TestHubOutboxBounded(t *testing.T) {
	h := NewHub()
	for i := 0; i < EventOutboxCapacity+10; i++ {
		mustCreateSession(t, h, "one", nil)
	}
	events := h.takeEvents()
	if len(events) != EventOutboxCapacity || h.Revision() != EventOutboxCapacity+11 {
		t.Fatalf("outbox=%d revision=%d", len(events), h.Revision())
	}
	mustCreateSession(t, h, "later", nil)
	later := h.takeEvents()
	if len(later) != 1 || later[0].Revision <= events[len(events)-1].Revision+1 {
		t.Fatal("overflow did not preserve detectable gap")
	}
}

type eventWriterFunc func(uint64, *net.UDPAddr, protocol.VoicePacket) error

func (f eventWriterFunc) WritePacket(id uint64, addr *net.UDPAddr, p protocol.VoicePacket) error {
	return f(id, addr, p)
}

func TestDispatcherContinuesAfterRecipientErrorAndStops(t *testing.T) {
	h := NewHub()
	one := mustCreateSession(t, h, "one", nil)
	two := mustCreateSession(t, h, "two", nil)
	h.takeEvents()
	if err := h.JoinChannel(one.ID, DefaultChannelID); err != nil {
		t.Fatal(err)
	}
	if err := h.JoinChannel(two.ID, DefaultChannelID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	delivered := make(chan protocol.VoicePacket, 4)
	done := make(chan error, 1)
	go func() {
		done <- DispatchEvents(ctx, h, eventWriterFunc(func(id uint64, addr *net.UDPAddr, p protocol.VoicePacket) error {
			if id == one.ID {
				return errors.New("recipient unavailable")
			}
			// Hub access from writer would deadlock if dispatch held its mutex.
			_ = h.Count()
			delivered <- p
			return nil
		}))
	}()
	var previous domain.StateRevision
	for i := 0; i < 2; i++ {
		select {
		case p := <-delivered:
			e, err := protocol.DecodeStateEvent(p.Payload)
			if err != nil {
				t.Fatal(err)
			}
			if p.RequestID != 0 || p.SessionID != two.ID || e.Revision <= previous {
				t.Fatalf("bad delivery %+v", p)
			}
			previous = e.Revision
		case <-time.After(time.Second):
			t.Fatal("dispatcher blocked")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("dispatcher leaked")
	}
}

func TestSetAudioStatePublishesParticipantFlagsOnce(t *testing.T) {
	hub := NewHub()
	session, _, err := hub.CreateSessionReplacingEndpoint("alice", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9})
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.JoinChannel(session.ID, DefaultChannelID); err != nil {
		t.Fatal(err)
	}
	_ = hub.takeEvents()
	revision := hub.Revision()
	if err := hub.SetAudioState(session.ID, true, true); err != nil {
		t.Fatal(err)
	}
	if hub.Revision() != revision+1 {
		t.Fatalf("revision = %d, want %d", hub.Revision(), revision+1)
	}
	if err := hub.SetAudioState(session.ID, true, true); err != nil {
		t.Fatal(err)
	}
	if hub.Revision() != revision+1 {
		t.Fatal("unchanged audio state advanced the revision")
	}
	events := hub.takeEvents()
	if len(events) != 1 || events[0].Kind != domain.ParticipantAudio || events[0].SessionID != session.ID || !events[0].Muted || !events[0].Deafened {
		t.Fatalf("events = %+v", events)
	}
	snapshot := hub.ClientSnapshot()
	if len(snapshot.Participants) != 1 || !snapshot.Participants[0].Muted || !snapshot.Participants[0].Deafened {
		t.Fatalf("snapshot participants = %+v", snapshot.Participants)
	}
}
