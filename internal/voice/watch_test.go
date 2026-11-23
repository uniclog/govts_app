package voice

import (
	"errors"
	"net"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

func watchAddr(port int) *net.UDPAddr {
	return &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: port}
}

func mustAuthSession(t *testing.T, hub *Hub, name string, port int, user int64, level uint16) Session {
	t.Helper()
	session, _, err := hub.CreateAuthenticatedSession(name, watchAddr(port), user, level, 0, false)
	if err != nil {
		t.Fatalf("CreateAuthenticatedSession(%s): %v", name, err)
	}
	return session
}

func mustWatcher(t *testing.T, hub *Hub, port int, user int64, level uint16) Watcher {
	t.Helper()
	watcher, _, err := hub.CreateWatcher(watchAddr(port), user, level)
	if err != nil {
		t.Fatalf("CreateWatcher(user %d): %v", user, err)
	}
	return watcher
}

func targetIDs(targets []noticeTarget) map[uint64]bool {
	ids := make(map[uint64]bool, len(targets))
	for _, target := range targets {
		ids[target.ID] = true
	}
	return ids
}

func TestWatcherIsNotAParticipant(t *testing.T) {
	hub := NewHub()
	session := mustAuthSession(t, hub, "alice", 5000, 1, 0)
	revision := hub.Revision()
	watcher := mustWatcher(t, hub, 5001, 2, 10)
	if watcher.ID == 0 || watcher.ID == session.ID {
		t.Fatalf("watcher ID = %d", watcher.ID)
	}
	if !hub.Known(watcher.ID) || !hub.Known(session.ID) || hub.Known(watcher.ID+session.ID) {
		t.Fatal("Known does not cover exactly sessions and watchers")
	}
	if _, ok := hub.Get(watcher.ID); ok {
		t.Fatal("a watcher is visible as a session")
	}
	if hub.Revision() != revision {
		t.Fatal("creating a watcher changed the state revision")
	}
	if participants := hub.ClientSnapshot().Participants; len(participants) != 1 || participants[0].SessionID != session.ID {
		t.Fatalf("snapshot participants = %+v", participants)
	}
	if hub.Count() != 1 {
		t.Fatalf("online count = %d, want 1", hub.Count())
	}
	if got, ok := hub.Watcher(watcher.ID); !ok || got.UserID != 2 || got.JoinLevel != 10 {
		t.Fatalf("Watcher() = %+v, %t", got, ok)
	}
}

func TestWatcherLifecycle(t *testing.T) {
	hub := NewHub()
	watcher := mustWatcher(t, hub, 5001, 2, 0)

	hub.ApplyJoinLevel(2, 30)
	if got, _ := hub.Watcher(watcher.ID); got.JoinLevel != 30 {
		t.Fatalf("join level after ApplyJoinLevel = %d, want 30", got.JoinLevel)
	}
	// A participant at the same endpoint replaces the watcher.
	_, replaced, err := hub.CreateAuthenticatedSession("bob", watchAddr(5001), 2, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(replaced) != 1 || replaced[0] != watcher.ID || hub.Known(watcher.ID) {
		t.Fatalf("replaced = %v, watcher known = %t", replaced, hub.Known(watcher.ID))
	}
	// And a watcher at a participant's endpoint replaces the participant.
	other := mustAuthSession(t, hub, "carol", 5002, 3, 0)
	second, replaced, err := hub.CreateWatcher(watchAddr(5002), 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(replaced) != 1 || replaced[0] != other.ID {
		t.Fatalf("replaced = %v, want %d", replaced, other.ID)
	}
	// A ban removes the watchers of the account.
	if removed := hub.RemoveUserSessions(3); len(removed) != 1 || removed[0] != second.ID || hub.Known(second.ID) {
		t.Fatalf("RemoveUserSessions = %v", removed)
	}
	// Watchers expire like sessions.
	third := mustWatcher(t, hub, 5003, 4, 0)
	if !hub.touchWatcher(third.ID, watchAddr(5003), time.Now()) {
		t.Fatal("touch from the watcher's own endpoint failed")
	}
	if hub.touchWatcher(third.ID, watchAddr(5004), time.Now()) {
		t.Fatal("touch from another endpoint accepted")
	}
	removed := hub.RemoveInactive(time.Now().Add(time.Minute), 30*time.Second)
	found := false
	for _, session := range removed {
		found = found || session.ID == third.ID
	}
	if !found || hub.Known(third.ID) {
		t.Fatalf("inactive watcher not removed: %+v", removed)
	}
}

func TestWatcherLimitsPerAccount(t *testing.T) {
	hub := NewHub()
	for i := 0; i < MaxSessionsPerAccount; i++ {
		mustWatcher(t, hub, 6000+i, 9, 0)
	}
	if _, _, err := hub.CreateWatcher(watchAddr(6100), 9, 0); !errors.Is(err, ErrAccountSessionLimit) {
		t.Fatalf("fifth watcher: %v, want ErrAccountSessionLimit", err)
	}
	hub.SetMaxSessions(MaxSessionsPerAccount + 1)
	mustWatcher(t, hub, 6200, 10, 0)
	if _, _, err := hub.CreateWatcher(watchAddr(6201), 11, 0); !errors.Is(err, ErrServerFull) {
		t.Fatalf("watcher over the server limit: %v, want ErrServerFull", err)
	}
}

func TestMessageRecipients(t *testing.T) {
	hub := NewHub()
	private, err := hub.CreateChannel(domain.Channel{Name: "private", MinJoinLevel: 25})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	enable := func(session Session, flags byte) {
		t.Helper()
		if _, _, err := hub.SetNoticePrefs(session.ID, flags, now); err != nil {
			t.Fatal(err)
		}
	}
	sender := mustAuthSession(t, hub, "sender", 7000, 1, 50)
	senderOther := mustAuthSession(t, hub, "sender2", 7001, 1, 50)
	inChannel := mustAuthSession(t, hub, "member", 7002, 2, 25)
	legacyInChannel := mustAuthSession(t, hub, "legacy", 7003, 3, 25)
	subscribed := mustAuthSession(t, hub, "fan", 7004, 4, 25)
	subscribedLow := mustAuthSession(t, hub, "low", 7005, 5, 0)
	unsubscribed := mustAuthSession(t, hub, "quiet", 7006, 6, 25)
	for _, session := range []Session{sender, senderOther, inChannel, legacyInChannel} {
		if err := hub.JoinChannel(session.ID, private.ID); err != nil {
			t.Fatal(err)
		}
	}
	enable(sender, 0)
	enable(senderOther, protocol.NoticeSubscribed)
	enable(inChannel, 0)
	enable(subscribed, protocol.NoticeSubscribed)
	enable(subscribedLow, protocol.NoticeSubscribed)
	enable(unsubscribed, 0)
	watcherHigh := mustWatcher(t, hub, 7100, 7, 25)
	watcherLow := mustWatcher(t, hub, 7101, 8, 0)
	senderWatcher := mustWatcher(t, hub, 7102, 1, 50)

	recipients, name := hub.messageRecipients(1, domain.ChatTarget{Kind: domain.ChatChannel, ID: int64(private.ID)})
	if name != "private" {
		t.Fatalf("channel name = %q", name)
	}
	want := map[uint64]bool{inChannel.ID: true, subscribed.ID: true, watcherHigh.ID: true}
	got := targetIDs(recipients)
	if len(got) != len(want) {
		t.Fatalf("channel recipients = %v, want %v", got, want)
	}
	for id := range want {
		if !got[id] {
			t.Fatalf("channel recipients = %v, missing %d", got, id)
		}
	}
	for _, id := range []uint64{sender.ID, senderOther.ID, legacyInChannel.ID, subscribedLow.ID, unsubscribed.ID, watcherLow.ID, senderWatcher.ID} {
		if got[id] {
			t.Fatalf("channel recipients include %d", id)
		}
	}

	direct, name := hub.messageRecipients(1, domain.ChatTarget{Kind: domain.ChatDirect, ID: 7})
	if name != "" || len(direct) != 1 || direct[0].ID != watcherHigh.ID {
		t.Fatalf("direct recipients = %+v, %q", direct, name)
	}
	if missing, _ := hub.messageRecipients(1, domain.ChatTarget{Kind: domain.ChatChannel, ID: 999}); len(missing) != 0 {
		t.Fatalf("recipients of a missing channel = %+v", missing)
	}

	arrivals := targetIDs(hub.arrivalRecipients(1))
	for _, id := range []uint64{inChannel.ID, subscribed.ID, subscribedLow.ID, unsubscribed.ID, watcherHigh.ID, watcherLow.ID} {
		if !arrivals[id] {
			t.Fatalf("arrival recipients %v miss %d", arrivals, id)
		}
	}
	for _, id := range []uint64{sender.ID, senderOther.ID, legacyInChannel.ID, senderWatcher.ID} {
		if arrivals[id] {
			t.Fatalf("arrival recipients include %d", id)
		}
	}
}

func TestArrivalLimitAndExitStreak(t *testing.T) {
	hub := NewHub()
	start := time.Now()
	at := func(offset time.Duration) time.Time { return start.Add(offset) }
	announce := func(name string, port int, offset time.Duration) bool {
		t.Helper()
		session := mustAuthSession(t, hub, name, port, 1, 0)
		_, ok, err := hub.SetNoticePrefs(session.ID, protocol.NoticeAnnounce, at(offset))
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	leave := func(port int, offset time.Duration) {
		t.Helper()
		for _, session := range hub.Inspect().Sessions {
			if session.Addr.Port == port {
				hub.Remove(session.ID)
			}
		}
		hub.ForgetArrival(1, at(offset))
	}

	step := func(name string, port int, offset time.Duration, want bool) {
		t.Helper()
		if got := announce(name, port, offset); got != want {
			t.Fatalf("%s at %s: announced=%t, want %t", name, offset, got, want)
		}
	}
	drop := func(port int) {
		for _, session := range hub.Inspect().Sessions {
			if session.Addr.Port == port {
				hub.Remove(session.ID)
			}
		}
	}

	step("first", 8001, 0, true)
	// A second request of the same session announces nothing.
	first := hub.Inspect().Sessions[0]
	if _, ok, _ := hub.SetNoticePrefs(first.ID, protocol.NoticeAnnounce, at(2*time.Minute)); ok {
		t.Fatal("one session announced twice")
	}
	// A reconnect after a lost connection waits the full minute.
	drop(8001)
	step("lost connection", 8002, 30*time.Second, false)

	// The first explicit exit: the next entry is announced 3 s later.
	leave(8002, 40*time.Second)
	step("2 s after exit 1", 8003, 42*time.Second, false)
	drop(8003)
	step("4 s after exit 1", 8004, 44*time.Second, true)
	// The exit let one entry through; a lost connection waits again.
	drop(8004)
	step("lost connection after it", 8005, 46*time.Second, false)

	// The second exit within the streak window: 15 s.
	leave(8005, 47*time.Second)
	step("10 s after exit 2", 8006, 57*time.Second, false)
	drop(8006)
	step("16 s after exit 2", 8007, 63*time.Second, true)

	// The third exit: only the minute rule, counted from the last arrival.
	leave(8007, 64*time.Second)
	step("56 s after the last arrival", 8008, 119*time.Second, false)
	drop(8008)
	step("61 s after the last arrival", 8009, 124*time.Second, true)

	// After the streak window a single exit is fast again.
	leave(8009, 5*time.Minute)
	step("4 s after a lone exit", 8010, 5*time.Minute+4*time.Second, true)
}

func TestExitWithAnotherSessionKeepsTheLimit(t *testing.T) {
	hub := NewHub()
	now := time.Now()
	first := mustAuthSession(t, hub, "one", 9001, 1, 0)
	if _, ok, _ := hub.SetNoticePrefs(first.ID, protocol.NoticeAnnounce, now); !ok {
		t.Fatal("first arrival not announced")
	}
	mustAuthSession(t, hub, "two", 9002, 1, 0)
	hub.Remove(first.ID)
	// The account is still on the server, so it has not left.
	hub.ForgetArrival(1, now.Add(time.Second))
	third := mustAuthSession(t, hub, "three", 9003, 1, 0)
	if _, ok, _ := hub.SetNoticePrefs(third.ID, protocol.NoticeAnnounce, now.Add(10*time.Second)); ok {
		t.Fatal("arrival announced while the account never left")
	}
}

func TestChatWorkerNotifiesEachMessageOnce(t *testing.T) {
	worker := &chatWorker{notified: make(map[int64]struct{})}
	if !worker.firstNotice(1) || worker.firstNotice(1) {
		t.Fatal("message 1 is not notified exactly once")
	}
	if worker.firstNotice(0) || worker.firstNotice(-1) {
		t.Fatal("invalid message ids notified")
	}
	for id := int64(2); id <= int64(len(worker.notifiedRing))+1; id++ {
		if !worker.firstNotice(id) {
			t.Fatalf("message %d not notified", id)
		}
	}
	if len(worker.notified) != len(worker.notifiedRing) {
		t.Fatalf("remembered %d ids, want %d", len(worker.notified), len(worker.notifiedRing))
	}
	// The ring forgot the oldest id, and still knows the newest.
	if !worker.firstNotice(1) || worker.firstNotice(int64(len(worker.notifiedRing))+1) {
		t.Fatal("ring does not keep exactly the latest ids")
	}
}

func TestVoiceTrafficSkipsTheWatcherLookup(t *testing.T) {
	for _, packetType := range []uint8{protocol.PacketVoiceBundleV2, protocol.PacketVoiceSummary, protocol.PacketVoiceReport, protocol.PacketVoiceState, protocol.PacketVoiceStateAck, protocol.PacketVoiceStateRequest} {
		if !isVoiceTraffic(packetType) {
			t.Fatalf("type %d is not voice traffic", packetType)
		}
	}
	for _, packetType := range []uint8{protocol.PacketHeartbeat, protocol.PacketDisconnect, protocol.PacketChatRequest, protocol.PacketNoticePrefs, protocol.PacketJoinChannel} {
		if isVoiceTraffic(packetType) {
			t.Fatalf("type %d must reach the watcher handler", packetType)
		}
	}
}
