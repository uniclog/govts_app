package wailsui

import (
	"strings"
	"sync"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/apppopup"
	"uniclog.io/sonoryx/internal/clientapp"
	"uniclog.io/sonoryx/internal/clientsettings"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

type popupRecorder struct {
	mu       sync.Mutex
	messages []apppopup.Message
	focused  bool
}

func (r *popupRecorder) show(message apppopup.Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.messages = append(r.messages, message)
	return nil
}

func (r *popupRecorder) take() []apppopup.Message {
	r.mu.Lock()
	defer r.mu.Unlock()
	messages := r.messages
	r.messages = nil
	return messages
}

const testServer = "127.0.0.1:9000"

// newNoticeService returns a service whose notices go to a recorder. The
// sound volume is zero, so a cue never opens a playback device.
func newNoticeService(t *testing.T, subscribed bool) (*Service, *popupRecorder) {
	t.Helper()
	client := clientapp.New(clientapp.Options{})
	if err := client.SetSoundVolume(0); err != nil {
		t.Fatal(err)
	}
	s := NewService(client)
	recorder := &popupRecorder{}
	s.noticeWindow = &noticeWindow{focused: func() bool { return recorder.focused }, show: recorder.show, open: func() {}}
	s.recentServers = []clientsettings.RecentServer{{Address: testServer, Alias: "ReCon", Subscribed: subscribed}}
	s.SetNoticeTexts(map[string]string{
		"arrival": "{name} в сети", "arrivalBody": "Сервер «{server}»", "attachment": "📎 Вложение",
		"direct": "личное сообщение", "many": "Новых сообщений: {count}",
	})
	return s, recorder
}

func arrival(user int64, session uint64) protocol.Notice {
	return protocol.Notice{Kind: protocol.NoticeArrival, UserID: user, Name: "bob", SessionID: session}
}

func channelMessage(id int64, text string) protocol.Notice {
	return protocol.Notice{Kind: protocol.NoticeMessage, UserID: 2, Name: "bob", Target: domain.ChatTarget{Kind: domain.ChatChannel, ID: 1}, ChannelName: "general", MessageID: id, Text: text}
}

func TestArrivalNoticeNeedsSubscription(t *testing.T) {
	s, recorder := newNoticeService(t, false)
	s.handleNotice(testServer, false, arrival(2, 10))
	if got := recorder.take(); len(got) != 0 {
		t.Fatalf("unsubscribed server popped up: %+v", got)
	}

	s, recorder = newNoticeService(t, true)
	s.handleNotice(testServer, false, arrival(2, 10))
	got := recorder.take()
	if len(got) != 1 || got[0].Title != "bob в сети" || got[0].Body != "Сервер «ReCon»" {
		t.Fatalf("arrival popup = %+v", got)
	}
	// The same arrival through the watcher and the main session shows once,
	// a new connection of the same person shows again.
	s.handleNotice(testServer, true, arrival(2, 10))
	s.handleNotice(testServer, false, arrival(2, 11))
	if got := recorder.take(); len(got) != 1 {
		t.Fatalf("popups after a duplicate and a new arrival = %d, want 1", len(got))
	}
	// An older server sends no session id: such arrivals are never merged.
	s.handleNotice(testServer, false, arrival(2, 0))
	s.handleNotice(testServer, false, arrival(2, 0))
	if got := recorder.take(); len(got) != 2 {
		t.Fatalf("arrivals without a session id = %d popups, want 2", len(got))
	}
}

func TestArrivalNoticeRespectsWindowAndMute(t *testing.T) {
	s, recorder := newNoticeService(t, true)
	recorder.focused = true
	s.handleNotice(testServer, false, arrival(2, 20))
	if got := recorder.take(); len(got) != 0 {
		t.Fatal("arrival popped up over the active window of its own server")
	}
	// A watcher reports a server the window does not show.
	s.handleNotice(testServer, true, arrival(2, 21))
	if got := recorder.take(); len(got) != 1 {
		t.Fatalf("watcher arrival with an active window = %d popups, want 1", len(got))
	}
	recorder.focused = false
	if err := s.SetNotificationsMuted(true); err != nil {
		t.Fatal(err)
	}
	s.handleNotice(testServer, true, arrival(2, 22))
	if got := recorder.take(); len(got) != 0 {
		t.Fatal("muted notifications popped up")
	}
}

func TestMessageNoticeOnScreenOnlySounds(t *testing.T) {
	s, recorder := newNoticeService(t, false)
	recorder.focused = true
	s.SetActiveChat("channel", "1")
	s.handleNotice(testServer, false, channelMessage(1, "hi"))
	if got := recorder.take(); len(got) != 0 {
		t.Fatal("message in the chat on screen popped up")
	}
	if s.lastCue.IsZero() {
		t.Fatal("message in the chat on screen did not play the cue")
	}
	// Another conversation, or the same one through a watcher, pops up.
	s.SetActiveChat("direct", "5")
	s.handleNotice(testServer, false, channelMessage(2, "hi"))
	if got := recorder.take(); len(got) != 1 || got[0].Title != "bob · general" || got[0].Body != "hi" || got[0].OnClick == nil {
		t.Fatalf("popup = %+v", got)
	}
}

func TestMessageNoticeTitlesAndSummary(t *testing.T) {
	s, recorder := newNoticeService(t, true)
	direct := protocol.Notice{Kind: protocol.NoticeMessage, UserID: 2, Name: "bob", Target: domain.ChatTarget{Kind: domain.ChatDirect, ID: 2}, MessageID: 7, Attachments: true}
	s.handleNotice(testServer, true, direct)
	got := recorder.take()
	if len(got) != 1 || got[0].Title != "bob · личное сообщение · ReCon" || got[0].Body != "📎 Вложение" {
		t.Fatalf("direct popup = %+v", got)
	}
	// The popup is still up: the next messages turn it into a summary.
	s.handleNotice(testServer, false, channelMessage(8, "second"))
	s.handleNotice(testServer, false, channelMessage(9, "third"))
	got = recorder.take()
	if len(got) != 2 || got[1].Title != "Новых сообщений: 3" || got[1].Body != "bob · general: third" {
		t.Fatalf("summary popups = %+v", got)
	}
	// A duplicate message changes nothing.
	s.handleNotice(testServer, true, channelMessage(9, "third"))
	if got := recorder.take(); len(got) != 0 {
		t.Fatal("duplicate message popped up")
	}
	// Once the popup is gone, the next message stands alone again.
	s.noticeMu.Lock()
	s.burstUntil = time.Now().Add(-time.Second)
	s.noticeMu.Unlock()
	s.handleNotice(testServer, false, channelMessage(10, "later"))
	if got := recorder.take(); len(got) != 1 || strings.HasPrefix(got[0].Title, "Новых") {
		t.Fatalf("message after the summary = %+v", got)
	}
}

func TestMessageCueIsSpaced(t *testing.T) {
	s, _ := newNoticeService(t, false)
	s.playMessageCue()
	first := s.lastCue
	s.playMessageCue()
	if !s.lastCue.Equal(first) {
		t.Fatal("a second cue within the interval was played")
	}
	s.noticeMu.Lock()
	s.lastCue = time.Now().Add(-messageCueInterval)
	s.noticeMu.Unlock()
	s.playMessageCue()
	if !time.Now().Add(-time.Second).Before(s.lastCue) {
		t.Fatal("the cue did not play after the interval")
	}
}

func TestMutedMessagesStaySilent(t *testing.T) {
	s, recorder := newNoticeService(t, true)
	s.notificationsMuted = true
	s.handleNotice(testServer, true, channelMessage(1, "hi"))
	if got := recorder.take(); len(got) != 0 || !s.lastCue.IsZero() {
		t.Fatalf("muted message: popups=%d cue=%v", len(got), s.lastCue)
	}
}

func TestPresenceStatusValidation(t *testing.T) {
	s := NewService(clientapp.New(clientapp.Options{}))
	if s.PresenceStatus() != clientsettings.PresenceOnline {
		t.Fatalf("default presence = %q", s.PresenceStatus())
	}
	if err := s.SetPresenceStatus("away"); err == nil {
		t.Fatal("unknown status accepted")
	}
	if err := s.SetPresenceStatus(clientsettings.PresenceInvisible); err != nil || s.PresenceStatus() != clientsettings.PresenceInvisible {
		t.Fatalf("SetPresenceStatus(invisible): %v, now %q", err, s.PresenceStatus())
	}
}

func TestFillNotice(t *testing.T) {
	if got := fillNotice("{name} · {server} · {name}", map[string]string{"name": "a", "server": "b"}); got != "a · b · a" {
		t.Fatalf("fillNotice = %q", got)
	}
}
