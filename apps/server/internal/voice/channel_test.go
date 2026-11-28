package voice

import (
	"errors"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"

	"uniclog.io/sonoryx/internal/domain"
)

func TestHubStartsWithDefaultChannel(t *testing.T) {
	hub := NewHub()

	channels := hub.ListChannels()
	if len(channels) != 1 {
		t.Fatalf("ListChannels() count = %d, want 1", len(channels))
	}
	channel := channels[0]
	if channel.ID != DefaultChannelID || channel.Name != DefaultChannelName {
		t.Fatalf("default channel = %+v", channel)
	}
	if channel.ParentID != 0 || channel.Type != domain.ChannelTypePermanent {
		t.Fatalf("default channel metadata = %+v", channel)
	}
	if channel.Audio != domain.DefaultAudioProfile() {
		t.Fatalf("default audio profile = %+v", channel.Audio)
	}
	if hub.Revision() != 1 {
		t.Fatalf("initial revision = %d, want 1", hub.Revision())
	}
}

func TestHubCreatesAndSortsChannelHierarchy(t *testing.T) {
	hub := NewHub()

	late := mustCreateChannel(t, hub, domain.Channel{Name: "late", Position: 20})
	early := mustCreateChannel(t, hub, domain.Channel{Name: "early", Position: 10})
	child := mustCreateChannel(t, hub, domain.Channel{
		ParentID: late.ID,
		Name:     "child",
		Position: 1,
	})

	if late.ID != 2 || early.ID != 3 || child.ID != 4 {
		t.Fatalf("channel IDs = (%d, %d, %d), want (2, 3, 4)", late.ID, early.ID, child.ID)
	}

	channels := hub.ListChannels()
	gotIDs := make([]domain.ChannelID, len(channels))
	for i, channel := range channels {
		gotIDs[i] = channel.ID
	}
	wantIDs := []domain.ChannelID{DefaultChannelID, early.ID, late.ID, child.ID}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("sorted channel IDs = %v, want %v", gotIDs, wantIDs)
	}

	channels[0].Name = "changed outside hub"
	stored, ok := hub.GetChannel(DefaultChannelID)
	if !ok {
		t.Fatal("default channel not found")
	}
	if stored.Name != DefaultChannelName {
		t.Fatalf("mutating list changed stored channel name to %q", stored.Name)
	}
}

func TestHubRejectsInvalidChannelMetadata(t *testing.T) {
	tests := []struct {
		name    string
		channel domain.Channel
	}{
		{name: "assigned ID", channel: domain.Channel{ID: 99, Name: "room"}},
		{name: "empty name", channel: domain.Channel{}},
		{name: "untrimmed name", channel: domain.Channel{Name: " room"}},
		{name: "long name", channel: domain.Channel{Name: strings.Repeat("a", domain.MaxChannelNameBytes+1)}},
		{name: "invalid name UTF-8", channel: domain.Channel{Name: string([]byte{0xff})}},
		{
			name: "long topic",
			channel: domain.Channel{
				Name:  "room",
				Topic: strings.Repeat("a", domain.MaxChannelTopicBytes+1),
			},
		},
		{
			name: "long description",
			channel: domain.Channel{
				Name:        "room",
				Description: strings.Repeat("a", domain.MaxChannelDescriptionBytes+1),
			},
		},
		{name: "unsupported type", channel: domain.Channel{Name: "room", Type: 99}},
		{
			name: "unsupported audio",
			channel: domain.Channel{
				Name:  "room",
				Audio: domain.AudioProfile{Codec: domain.AudioCodecOpus},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hub := NewHub()
			_, err := hub.CreateChannel(test.channel)
			if !errors.Is(err, ErrInvalidChannel) {
				t.Fatalf("CreateChannel() error = %v, want %v", err, ErrInvalidChannel)
			}
			if len(hub.ListChannels()) != 1 {
				t.Fatal("invalid channel changed registry")
			}
		})
	}
}

func TestHubValidatesChannelParentDepthAndNames(t *testing.T) {
	hub := NewHub()

	if _, err := hub.CreateChannel(domain.Channel{ParentID: 999, Name: "orphan"}); !errors.Is(err, ErrChannelNotFound) {
		t.Fatalf("missing parent error = %v, want %v", err, ErrChannelNotFound)
	}

	if _, err := hub.CreateChannel(domain.Channel{Name: "DEFAULT"}); !errors.Is(err, ErrChannelNameTaken) {
		t.Fatalf("duplicate sibling name error = %v, want %v", err, ErrChannelNameTaken)
	}

	left := mustCreateChannel(t, hub, domain.Channel{Name: "left"})
	right := mustCreateChannel(t, hub, domain.Channel{Name: "right"})
	mustCreateChannel(t, hub, domain.Channel{ParentID: left.ID, Name: "room"})
	mustCreateChannel(t, hub, domain.Channel{ParentID: right.ID, Name: "room"})

	parentID := DefaultChannelID
	for depth := 2; depth <= domain.MaxChannelDepth; depth++ {
		channel := mustCreateChannel(t, hub, domain.Channel{
			ParentID: parentID,
			Name:     "depth-" + string(rune('0'+depth)),
		})
		parentID = channel.ID
	}
	if _, err := hub.CreateChannel(domain.Channel{ParentID: parentID, Name: "too-deep"}); !errors.Is(err, ErrInvalidChannel) {
		t.Fatalf("depth error = %v, want %v", err, ErrInvalidChannel)
	}
}

func TestHubDefensivelyRejectsCyclicParentChain(t *testing.T) {
	hub := NewHub()
	left := mustCreateChannel(t, hub, domain.Channel{Name: "left"})
	right := mustCreateChannel(t, hub, domain.Channel{Name: "right"})

	hub.mu.Lock()
	hub.channels[left.ID].ParentID = right.ID
	hub.channels[right.ID].ParentID = left.ID
	hub.mu.Unlock()

	_, err := hub.CreateChannel(domain.Channel{ParentID: left.ID, Name: "child"})
	if !errors.Is(err, ErrInvalidChannel) {
		t.Fatalf("CreateChannel() error = %v, want cycle rejection", err)
	}
}

func TestHubEnforcesChannelCapacity(t *testing.T) {
	hub := NewHub()
	channel := mustCreateChannel(t, hub, domain.Channel{Name: "small", MaxUsers: 1})
	alice := mustCreateSession(t, hub, "alice", nil)
	bob := mustCreateSession(t, hub, "bob", nil)

	if err := hub.JoinChannel(alice.ID, channel.ID); err != nil {
		t.Fatal(err)
	}
	revision := hub.Revision()
	if err := hub.JoinChannel(alice.ID, channel.ID); err != nil {
		t.Fatalf("idempotent JoinChannel() error = %v", err)
	}
	if hub.Revision() != revision {
		t.Fatal("idempotent join changed revision")
	}
	if err := hub.JoinChannel(bob.ID, channel.ID); !errors.Is(err, ErrChannelFull) {
		t.Fatalf("JoinChannel() error = %v, want %v", err, ErrChannelFull)
	}
	bobSnapshot, _ := hub.Get(bob.ID)
	if bobSnapshot.ChannelID != 0 {
		t.Fatalf("failed join moved Bob to channel %d", bobSnapshot.ChannelID)
	}
}

func TestHubRevisionTracksVisibleStateOnly(t *testing.T) {
	hub := NewHub()
	wantRevision := domain.StateRevision(1)
	assertRevision(t, hub, wantRevision)

	session := mustCreateSession(t, hub, "alice", nil)
	wantRevision++
	assertRevision(t, hub, wantRevision)

	if err := hub.Touch(session.ID); err != nil {
		t.Fatal(err)
	}
	if err := hub.UpdateAddr(session.ID, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 9000}); err != nil {
		t.Fatal(err)
	}
	assertRevision(t, hub, wantRevision)

	if err := hub.Rename(session.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	assertRevision(t, hub, wantRevision)
	if err := hub.Rename(session.ID, "alice-2"); err != nil {
		t.Fatal(err)
	}
	wantRevision++
	assertRevision(t, hub, wantRevision)

	channel := mustCreateChannel(t, hub, domain.Channel{Name: "music"})
	wantRevision++
	assertRevision(t, hub, wantRevision)
	if err := hub.JoinChannel(session.ID, channel.ID); err != nil {
		t.Fatal(err)
	}
	wantRevision++
	assertRevision(t, hub, wantRevision)

	if _, ok := hub.Remove(999); ok {
		t.Fatal("Remove() removed unknown session")
	}
	assertRevision(t, hub, wantRevision)
	if _, ok := hub.Remove(session.ID); !ok {
		t.Fatal("Remove() did not remove session")
	}
	wantRevision++
	assertRevision(t, hub, wantRevision)
}

func TestHubParticipantsAreSortedDomainSnapshots(t *testing.T) {
	ids := []uint64{20, 10}
	next := 0
	hub := newHub(func() (uint64, error) {
		id := ids[next]
		next++
		return id, nil
	})
	channel := mustCreateChannel(t, hub, domain.Channel{Name: "music"})
	alice := mustCreateSession(t, hub, "alice", &net.UDPAddr{Port: 9001})
	bob := mustCreateSession(t, hub, "bob", &net.UDPAddr{Port: 9002})
	if err := hub.JoinChannel(alice.ID, channel.ID); err != nil {
		t.Fatal(err)
	}

	participants := hub.Participants()
	if len(participants) != 2 {
		t.Fatalf("Participants() count = %d, want 2", len(participants))
	}
	if participants[0].SessionID != bob.ID || participants[1].SessionID != alice.ID {
		t.Fatalf("participant order = %+v", participants)
	}
	if participants[1].ChannelID != channel.ID || participants[1].DisplayName != "alice" {
		t.Fatalf("Alice participant = %+v", participants[1])
	}
}

func TestHubOperationalSnapshotIsIndependent(t *testing.T) {
	hub := NewHub()
	channel := mustCreateChannel(t, hub, domain.Channel{Name: "main"})
	session := mustCreateSession(t, hub, "alice", &net.UDPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: 9001,
	})
	if err := hub.JoinChannel(session.ID, channel.ID); err != nil {
		t.Fatal(err)
	}

	snapshot := hub.Inspect()
	if snapshot.Revision != hub.Revision() || len(snapshot.Channels) != 2 || len(snapshot.Sessions) != 1 {
		t.Fatalf("Inspect() = %+v", snapshot)
	}
	snapshot.Channels[0].Name = "changed"
	snapshot.Sessions[0].Name = "changed"
	snapshot.Sessions[0].Addr.IP[0] = 10

	storedChannel, ok := hub.GetChannel(DefaultChannelID)
	if !ok || storedChannel.Name != DefaultChannelName {
		t.Fatalf("stored default channel = %+v, found=%v", storedChannel, ok)
	}
	storedSession, ok := hub.Get(session.ID)
	if !ok || storedSession.Name != "alice" || !storedSession.Addr.IP.Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("stored session = %+v, found=%v", storedSession, ok)
	}
}

func TestHubConcurrentJoinRemoveAndList(t *testing.T) {
	hub := NewHub()
	channel := mustCreateChannel(t, hub, domain.Channel{Name: "music"})
	const sessionCount = 100
	sessions := make([]Session, 0, sessionCount)
	for i := 0; i < sessionCount; i++ {
		sessions = append(sessions, mustCreateSession(t, hub, "client", nil))
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		<-start
		for _, session := range sessions {
			err := hub.JoinChannel(session.ID, channel.ID)
			if err != nil && !errors.Is(err, ErrSessionNotFound) {
				t.Errorf("JoinChannel() error = %v", err)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i, session := range sessions {
			if i%2 == 0 {
				hub.Remove(session.ID)
			}
		}
	}()
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < sessionCount; i++ {
			hub.ListChannels()
			hub.Participants()
			hub.SessionsInChannel(channel.ID)
		}
	}()

	close(start)
	wg.Wait()
	if len(hub.ListChannels()) != 2 {
		t.Fatalf("channel registry changed during concurrent access")
	}
}

func assertRevision(t *testing.T, hub *Hub, want domain.StateRevision) {
	t.Helper()
	if got := hub.Revision(); got != want {
		t.Fatalf("Revision() = %d, want %d", got, want)
	}
}
