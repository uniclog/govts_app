package client

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"uniclog.io/govts/internal/domain"
)

func liveTestState() *State {
	s := NewState(1, "alice")
	s.ReplaceSnapshot(domain.ServerSnapshot{Revision: 10, Info: domain.ServerInfo{Name: "server"},
		Channels:     []domain.Channel{{ID: 1, Name: "main", Type: domain.ChannelTypePermanent, Audio: domain.DefaultAudioProfile()}, {ID: 2, Name: "other", Type: domain.ChannelTypePermanent, Audio: domain.DefaultAudioProfile()}},
		Participants: []domain.Participant{{SessionID: 1, DisplayName: "alice", ChannelID: 1}, {SessionID: 2, DisplayName: "bob", ChannelID: 1}}})
	s.SetChannelID(1)
	return s
}

func TestLiveStateOrderGapAndSnapshotPublication(t *testing.T) {
	s := liveTestState()
	gen := s.Generation()
	move := domain.StateEvent{Kind: domain.ParticipantMoved, Revision: 11, SessionID: 2, ChannelID: 2}
	if !s.ApplyEvent(gen, move) {
		t.Fatal("next event not applied")
	}
	if s.ApplyEvent(gen, move) {
		t.Fatal("duplicate applied")
	}
	old := s.Snapshot()
	old.Revision = 10
	if !s.ReplaceSnapshotForGeneration(gen, old) || s.Snapshot().Revision != 11 {
		t.Fatal("snapshot rolled back event")
	}
	gap := domain.StateEvent{Kind: domain.ParticipantLeft, Revision: 13, SessionID: 2}
	if s.ApplyEvent(gen, gap) || s.SnapshotFresh() {
		t.Fatal("gap accepted")
	}
	move.Revision = 12
	if s.ApplyEvent(gen, move) {
		t.Fatal("applied while inconsistent")
	}
	if !s.ReplaceSnapshotForGeneration(gen, old) || s.SnapshotFresh() {
		t.Fatal("old resync repaired gap incorrectly")
	}
	latest := s.Snapshot()
	latest.Revision = 13
	latest.Participants = latest.Participants[:1]
	if !s.ReplaceSnapshotForGeneration(gen, latest) || !s.SnapshotFresh() {
		t.Fatal("latest snapshot not published")
	}
	s.InvalidateSession(ConnectionReconnecting)
	s.StartSession(3)
	if s.ApplyEvent(gen, domain.StateEvent{Kind: domain.ParticipantLeft, Revision: 14, SessionID: 1}) {
		t.Fatal("old generation applied")
	}
}

func TestLiveSemanticFailureIsAtomicAndInitialEventsRemembered(t *testing.T) {
	s := liveTestState()
	gen := s.Generation()
	if s.ApplyEvent(gen, domain.StateEvent{Kind: domain.ParticipantMoved, Revision: 11, SessionID: 2, ChannelID: 999}) {
		t.Fatal("unknown channel accepted")
	}
	if s.Snapshot().Participants[1].ChannelID != 1 || s.Snapshot().Revision != 10 {
		t.Fatal("partial mutation")
	}
	s.StartSession(3)
	gen = s.Generation()
	s.ApplyEvent(gen, domain.StateEvent{Kind: domain.ParticipantLeft, Revision: 25, SessionID: 2})
	snap := s.Snapshot()
	snap.Revision = 24
	s.ReplaceSnapshotForGeneration(gen, snap)
	if s.SnapshotFresh() {
		t.Fatal("initial sync lost concurrent event")
	}
	snap.Revision = 25
	s.ReplaceSnapshotForGeneration(gen, snap)
	if !s.SnapshotFresh() {
		t.Fatal("initial sync did not recover")
	}
}

func TestParticipantChannelNotificationSounds(t *testing.T) {
	s := liveTestState()
	gen := s.Generation()
	if !s.ApplyEvent(gen, domain.StateEvent{Kind: domain.ParticipantMoved, Revision: 11, SessionID: 2, ChannelID: 2}) {
		t.Fatal("move out was not applied")
	}
	if sound := <-s.NotificationSounds(); sound.Kind != NotificationLeft {
		t.Fatalf("move out sound = %v", sound.Kind)
	}
	if !s.ApplyEvent(gen, domain.StateEvent{Kind: domain.ParticipantMoved, Revision: 12, SessionID: 2, ChannelID: 1}) {
		t.Fatal("move in was not applied")
	}
	if sound := <-s.NotificationSounds(); sound.Kind != NotificationJoined {
		t.Fatalf("move in sound = %v", sound.Kind)
	}

	self := liveTestState()
	if !self.ApplyEvent(self.Generation(), domain.StateEvent{Kind: domain.ParticipantMoved, Revision: 11, SessionID: 1, ChannelID: 2}) {
		t.Fatal("self move was not applied")
	}
	select {
	case sound := <-self.NotificationSounds():
		t.Fatalf("self move emitted sound %v", sound.Kind)
	default:
	}
}

func TestInvalidEventDoesNotEmitSound(t *testing.T) {
	s := liveTestState()
	if s.ApplyEvent(s.Generation(), domain.StateEvent{Kind: domain.ParticipantMoved, Revision: 11, SessionID: 2, ChannelID: 999}) {
		t.Fatal("invalid move was applied")
	}
	select {
	case sound := <-s.NotificationSounds():
		t.Fatalf("invalid move emitted sound %v", sound.Kind)
	default:
	}
}

func TestViewSubscriptionsCoalesceAndCleanUp(t *testing.T) {
	s := liveTestState()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, unsubscribe := s.Subscribe(ctx)
	for i := 0; i < 1000; i++ {
		s.SetConnectionStatus(ConnectionConnected)
	}
	if len(ch) != 1 {
		t.Fatalf("signals=%d", len(ch))
	}
	v := s.SnapshotView()
	v.Channels[0].Name = "mutated"
	v.Speaking[9] = true
	if s.SnapshotView().Channels[0].Name == "mutated" || s.SnapshotView().Speaking[9] {
		t.Fatal("view aliases state")
	}
	<-ch
	unsubscribe()
	unsubscribe()
	if _, ok := <-ch; ok {
		t.Fatal("subscription open")
	}
	other, _ := s.Subscribe(ctx)
	<-other
	cancel()
	select {
	case _, ok := <-other:
		if ok {
			t.Fatal("expected close")
		}
	case <-time.After(time.Second):
		t.Fatal("subscription leaked")
	}
	s.SetConnectionStatus(ConnectionDisconnected)
}

func TestSpeakingMembershipMuteAndExpiry(t *testing.T) {
	s := liveTestState()
	now := time.Now()
	if !s.ObserveSpeaking(2, []int16{600}, now) || !s.SnapshotView().Speaking[2] {
		t.Fatal("remote speaking missing")
	}
	s.Audio.SetDeafened(true)
	s.ObserveSpeaking(2, []int16{1000}, now)
	if !s.SnapshotView().Speaking[2] {
		t.Fatal("deafen disabled remote speaking")
	}
	s.ObserveSpeaking(1, []int16{600}, now)
	s.Audio.SetMuted(true)
	if s.SnapshotView().Speaking[1] {
		t.Fatal("mute did not reset local speaking")
	}
	s.ApplyEvent(s.Generation(), domain.StateEvent{Kind: domain.ParticipantMoved, Revision: 11, SessionID: 2, ChannelID: 2})
	if s.ObserveSpeaking(2, []int16{1000}, now) || s.SnapshotView().Speaking[2] {
		t.Fatal("late frame restored moved sender")
	}
	s.Audio.SetMuted(false)
	s.ObserveSpeaking(1, []int16{600}, now)
	s.clearSpeakingAt(now.Add(SpeakingHangover))
	if len(s.SnapshotView().Speaking) != 0 {
		t.Fatal("speaking never expired")
	}
}

func TestEventSnapshotAndSubscriptionConcurrency(t *testing.T) {
	s := liveTestState()
	gen := s.Generation()
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			channel := domain.ChannelID(i%2 + 1)
			s.ApplyEvent(gen, domain.StateEvent{Kind: domain.ParticipantMoved, Revision: domain.StateRevision(i + 11), SessionID: 2, ChannelID: channel})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			snapshot := s.Snapshot()
			s.ReplaceSnapshotForGeneration(gen, snapshot)
			s.Audio.SetMuted(i%2 == 0)
			_ = s.SnapshotView()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			ctx, cancel := context.WithCancel(context.Background())
			_, unsubscribe := s.Subscribe(ctx)
			cancel()
			unsubscribe()
		}
	}()
	wg.Wait()
	if s.Snapshot().Revision != 210 {
		t.Fatalf("revision rolled back: %d", s.Snapshot().Revision)
	}
}

type stateWriterFunc func([]byte) (int, error)

func (f stateWriterFunc) Write(p []byte) (int, error) { return f(p) }

func TestConsoleLiveLineAndOwnJoinAckOrdering(t *testing.T) {
	s := liveTestState()
	gen := s.Generation()
	if !s.ApplyEvent(gen, domain.StateEvent{Kind: domain.ParticipantMoved, Revision: 11, SessionID: 1, ChannelID: 2}) {
		t.Fatal("move rejected")
	}
	if !s.ConfirmChannel(gen, 1, 10) || s.SnapshotView().ChannelID != 2 {
		t.Fatal("late join ACK rolled back own channel")
	}
	stop := errors.New("captured")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var line string
	err := ConsoleStateLoop(ctx, s, stateWriterFunc(func(p []byte) (int, error) { line = string(p); return 0, stop }))
	if !errors.Is(err, stop) || !strings.Contains(line, "alice moved main → other") {
		t.Fatalf("console line=%q error=%v", line, err)
	}
}

func TestOwnJoinAckBeforeMoveDoesNotForceResync(t *testing.T) {
	s := liveTestState()
	gen := s.Generation()
	if !s.ConfirmChannel(gen, 2, 11) {
		t.Fatal("join ACK rejected")
	}
	if !s.SnapshotFresh() {
		t.Fatal("next-revision join ACK marked snapshot stale")
	}
	if len(s.resync) != 0 {
		t.Fatal("next-revision join ACK requested resync")
	}
	move := domain.StateEvent{Kind: domain.ParticipantMoved, Revision: 11, SessionID: 1, ChannelID: 2}
	if !s.ApplyEvent(gen, move) {
		t.Fatal("matching move event was rejected")
	}
	view := s.SnapshotView()
	if view.Revision != 11 || view.ChannelID != 2 || !view.SnapshotFresh {
		t.Fatalf("view after move = %+v", view)
	}
}

func TestAudioStateEventUpdatesParticipant(t *testing.T) {
	s := liveTestState()
	s.ObserveSpeaking(2, []int16{1000}, time.Now())
	event := domain.StateEvent{Kind: domain.ParticipantAudio, Revision: 11, SessionID: 2, Muted: true, Deafened: true}
	if !s.ApplyEvent(s.Generation(), event) {
		t.Fatal("audio event rejected")
	}
	view := s.SnapshotView()
	var bob domain.Participant
	for _, participant := range view.Participants {
		if participant.SessionID == 2 {
			bob = participant
		}
	}
	if !bob.Muted || !bob.Deafened || view.Speaking[2] {
		t.Fatalf("participant = %+v, speaking = %v", bob, view.Speaking)
	}
	if view.Participants[0].Muted || view.Participants[0].Deafened {
		t.Fatal("audio event changed another participant")
	}
}
