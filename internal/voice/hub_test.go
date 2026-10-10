package voice

import (
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/domain"
)

func TestHubReturnsIndependentSessionSnapshots(t *testing.T) {
	hub := NewHub()
	originalAddr := &net.UDPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: 5000,
	}

	created := mustCreateSession(t, hub, "alice", originalAddr)
	originalAddr.Port = 6000
	created.Name = "changed outside Hub"
	created.Addr.IP[0]++

	first, ok := hub.Get(created.ID)
	if !ok {
		t.Fatalf("session %d not found", created.ID)
	}
	if first.Name != "alice" {
		t.Fatalf("session name = %q, want %q", first.Name, "alice")
	}
	if first.Addr.Port != 5000 || !first.Addr.IP.Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("session address = %v, want 127.0.0.1:5000", first.Addr)
	}

	first.ChannelID = 999
	first.Addr.Port = 7000
	second, ok := hub.Get(created.ID)
	if !ok {
		t.Fatalf("session %d not found", created.ID)
	}
	if second.ChannelID != 0 || second.Addr.Port != 5000 {
		t.Fatalf("mutating snapshot changed Hub session: %+v", second)
	}
}

func TestCreateSessionReplacingEndpointIsAtomic(t *testing.T) {
	hub := NewHub()
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9001}
	first := mustCreateSession(t, hub, "alice", addr)
	revision := hub.Revision()
	second, replaced, err := hub.CreateSessionReplacingEndpoint("bob", addr)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID || len(replaced) != 1 || replaced[0] != first.ID {
		t.Fatalf("replacement = session %d, removed %v", second.ID, replaced)
	}
	if _, ok := hub.Get(first.ID); ok {
		t.Fatal("old endpoint session remains")
	}
	if hub.Count() != 1 || hub.Revision() != revision+2 {
		t.Fatalf("count/revision = %d/%d", hub.Count(), hub.Revision())
	}
}

func TestCreateSessionResumesSameIdentityAndPreservesScreenStream(t *testing.T) {
	hub := NewHub()
	channel := mustCreateChannel(t, hub, domain.Channel{Name: "main"})
	addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9001}
	first := mustCreateSession(t, hub, "alice", addr)
	if err := hub.JoinChannel(first.ID, channel.ID); err != nil {
		t.Fatal(err)
	}
	const streamID domain.StreamID = 42
	if _, err := hub.StartScreenShare(first.ID, streamID); err != nil {
		t.Fatal(err)
	}
	revision := hub.Revision()

	resumed, replaced, err := hub.CreateSessionReplacingEndpoint("alice", addr)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ID != first.ID || len(replaced) != 0 {
		t.Fatalf("resume = session %d, removed %v; want session %d", resumed.ID, replaced, first.ID)
	}
	if resumed.ChannelID != channel.ID {
		t.Fatalf("resumed channel = %d, want %d", resumed.ChannelID, channel.ID)
	}
	if _, ok := hub.ScreenStream(streamID); !ok {
		t.Fatal("resume removed active screen stream")
	}
	if hub.Count() != 1 || hub.Revision() != revision {
		t.Fatalf("count/revision = %d/%d, want 1/%d", hub.Count(), hub.Revision(), revision)
	}
}

func TestHubRemoveInactive(t *testing.T) {
	hub := NewHub()

	now := time.Now()

	active := mustCreateSession(t, hub, "alice", nil)
	inactive := mustCreateSession(t, hub, "bob", nil)

	if err := hub.touchAt(active.ID, now.Add(-5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := hub.touchAt(inactive.ID, now.Add(-40*time.Second)); err != nil {
		t.Fatal(err)
	}

	removed := hub.RemoveInactive(
		now,
		30*time.Second,
	)

	if len(removed) != 1 {
		t.Fatalf(
			"expected 1 removed session, got %d",
			len(removed),
		)
	}

	if removed[0].ID != inactive.ID {
		t.Fatalf(
			"expected session %d to be removed, got %d",
			inactive.ID,
			removed[0].ID,
		)
	}

	if _, ok := hub.Get(inactive.ID); ok {
		t.Fatal("inactive session still exists")
	}

	if _, ok := hub.Get(active.ID); !ok {
		t.Fatal("active session was removed")
	}
}

func TestHubConcurrentJoinTouchRoutingAndCleanup(t *testing.T) {
	hub := NewHub()
	sender := mustCreateSession(t, hub, "sender", nil)
	recipient := mustCreateSession(t, hub, "recipient", nil)
	music := mustCreateChannel(t, hub, domain.Channel{Name: "music"})

	if err := hub.JoinChannel(sender.ID, music.ID); err != nil {
		t.Fatal(err)
	}
	if err := hub.JoinChannel(recipient.ID, music.ID); err != nil {
		t.Fatal(err)
	}

	const iterations = 100
	start := make(chan struct{})
	var wg sync.WaitGroup
	for worker := 0; worker < 5; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			<-start

			for i := 0; i < iterations; i++ {
				switch worker {
				case 0:
					if err := hub.JoinChannel(sender.ID, music.ID); err != nil {
						t.Errorf("JoinChannel() error = %v", err)
						return
					}
				case 1:
					if err := hub.Touch(sender.ID); err != nil {
						t.Errorf("Touch() error = %v", err)
						return
					}
				case 2:
					recipients, err := hub.RecipientsFor(sender.ID)
					if err != nil {
						t.Errorf("RecipientsFor() error = %v", err)
						return
					}
					if len(recipients) != 1 {
						t.Errorf("RecipientsFor() count = %d, want 1", len(recipients))
						return
					}
				case 3:
					if removed := hub.RemoveInactive(time.Now(), time.Hour); len(removed) != 0 {
						t.Errorf("RemoveInactive() removed %d active sessions", len(removed))
						return
					}
				case 4:
					addr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5000 + i}
					if err := hub.UpdateAddr(sender.ID, addr); err != nil {
						t.Errorf("UpdateAddr() error = %v", err)
						return
					}
				}
			}
		}(worker)
	}

	close(start)
	wg.Wait()
}

func mustCreateChannel(t *testing.T, hub *Hub, channel domain.Channel) domain.Channel {
	t.Helper()

	created, err := hub.CreateChannel(channel)
	if err != nil {
		t.Fatalf("CreateChannel() error = %v", err)
	}
	return created
}

func TestHubCreatesNonZeroUniqueSessionIDs(t *testing.T) {
	hub := NewHub()
	const sessionCount = 1000

	seen := make(map[uint64]struct{}, sessionCount)
	for i := 0; i < sessionCount; i++ {
		session := mustCreateSession(t, hub, "client", nil)
		if session.ID == 0 {
			t.Fatal("CreateSession() returned reserved session ID 0")
		}
		if _, exists := seen[session.ID]; exists {
			t.Fatalf("CreateSession() returned duplicate session ID %d", session.ID)
		}
		seen[session.ID] = struct{}{}
	}
}

func TestHubRetriesReservedAndCollidingSessionIDs(t *testing.T) {
	ids := []uint64{7, 0, 7, 9}
	next := 0
	hub := newHub(func() (uint64, error) {
		id := ids[next]
		next++
		return id, nil
	})

	first := mustCreateSession(t, hub, "alice", nil)
	second := mustCreateSession(t, hub, "bob", nil)

	if first.ID != 7 || second.ID != 9 {
		t.Fatalf("session IDs = (%d, %d), want (7, 9)", first.ID, second.ID)
	}
}

func TestHubReturnsSessionIDGeneratorError(t *testing.T) {
	wantErr := errors.New("random source unavailable")
	hub := newHub(func() (uint64, error) {
		return 0, wantErr
	})

	_, err := hub.CreateSession("alice", nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("CreateSession() error = %v, want %v", err, wantErr)
	}
	if hub.Count() != 0 {
		t.Fatalf("Hub.Count() = %d, want 0", hub.Count())
	}
}

func mustCreateSession(
	t *testing.T,
	hub *Hub,
	name string,
	addr *net.UDPAddr,
) Session {
	t.Helper()

	session, err := hub.CreateSession(name, addr)
	if err != nil {
		t.Fatalf("CreateSession() error = %v", err)
	}
	return session
}
