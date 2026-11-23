package wailsui

import (
	"errors"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"uniclog.io/sonoryx/internal/apppopup"
	"uniclog.io/sonoryx/internal/clientapp"
	"uniclog.io/sonoryx/internal/clientsettings"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

// messageNoticeLifetime keeps a message popup long enough to read a line.
// Messages that arrive while it is up join it as one summary popup.
const messageNoticeLifetime = 6 * time.Second

// messageCueInterval spaces the incoming message sound. A busy subscription
// otherwise rings for every message, and without a session each cue opens
// the playback device.
const messageCueInterval = 4 * time.Second

// noticeWindow is what the desktop shell provides to notifications: whether
// the main window is in front, how to show a popup and how to bring the
// window back. None of them may wait for the UI thread.
type noticeWindow struct {
	focused func() bool
	show    func(apppopup.Message) error
	open    func()
	changed func()
}

// ConfigureNotifications connects server notices to popups. focused reports
// whether the main window is visible and active; changed tells the interface
// and the tray that the mute switch moved.
func ConfigureNotifications(s *Service, focused func() bool, show func(apppopup.Message) error, open func(), changed func()) {
	s.noticeMu.Lock()
	s.noticeWindow = &noticeWindow{focused: focused, show: show, open: open, changed: changed}
	s.noticeMu.Unlock()
	s.client.SetNoticeSink(s.handleNotice)
	s.client.SetNoticeSubscription(s.serverSubscribed)
	s.client.SetWatchChanged(func() {
		if changed != nil {
			changed()
		}
	})
	s.SyncWatchers()
}

// PresenceStatus is "online" or "invisible".
func (s *Service) PresenceStatus() string {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return clientsettings.NormalizePresence(s.presence)
}

// SetPresenceStatus changes the status. Going online on a server where the
// arrival was not announced yet announces it now.
func (s *Service) SetPresenceStatus(value string) error {
	if value != clientsettings.PresenceOnline && value != clientsettings.PresenceInvisible {
		return errors.New("неизвестный статус")
	}
	s.settingsMu.Lock()
	s.presence = value
	s.settingsMu.Unlock()
	s.client.SetNoticePresence(value == clientsettings.PresenceOnline)
	s.client.RefreshNotices()
	return s.saveSettings()
}

func (s *Service) NotificationsMuted() bool {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.notificationsMuted
}

func (s *Service) SetNotificationsMuted(value bool) error {
	s.settingsMu.Lock()
	changed := s.notificationsMuted != value
	s.notificationsMuted = value
	s.settingsMu.Unlock()
	if changed {
		s.notifyNoticeSettings()
	}
	return s.saveSettings()
}

// ToggleNotificationsMuted flips the switch for the tray and the hotkey and
// returns the new value.
func (s *Service) ToggleNotificationsMuted() bool {
	s.settingsMu.Lock()
	s.notificationsMuted = !s.notificationsMuted
	muted := s.notificationsMuted
	s.settingsMu.Unlock()
	s.notifyNoticeSettings()
	_ = s.saveSettings()
	return muted
}

func (s *Service) notifyNoticeSettings() {
	s.noticeMu.Lock()
	window := s.noticeWindow
	s.noticeMu.Unlock()
	if window != nil && window.changed != nil {
		window.changed()
	}
}

// SetServerSubscribed turns arrival and message notifications of a server on
// or off. Subscribing while connected keeps the connection password, so the
// background watcher can enter a protected server later.
func (s *Service) SetServerSubscribed(address string, subscribed bool) error {
	endpoint, err := clientapp.ParseServerEndpoint(strings.TrimSpace(address))
	if err != nil {
		return err
	}
	address = endpoint.String()
	secret := ""
	if subscribed && s.client.CurrentServer() == address {
		if password := s.client.ConnectionPassword(); password != "" {
			if secret, err = clientsettings.ProtectSecret(password); err != nil {
				log.Printf("protect watch password failed: server=%s error=%v", address, err)
				secret = ""
			}
		}
	}
	s.settingsMu.Lock()
	found := false
	for i := range s.recentServers {
		if s.recentServers[i].Address != address {
			continue
		}
		found = true
		s.recentServers[i].Subscribed = subscribed
		if !subscribed {
			s.recentServers[i].WatchSecret = ""
		} else if secret != "" {
			s.recentServers[i].WatchSecret = secret
		}
	}
	s.settingsMu.Unlock()
	if !found {
		return errors.New("сервер отсутствует в истории")
	}
	s.client.RefreshNotices()
	s.SyncWatchers()
	return s.saveSettings()
}

func (s *Service) serverSubscribed(address string) bool {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	for _, server := range s.recentServers {
		if server.Address == address {
			return server.Subscribed
		}
	}
	return false
}

// rememberVisit runs for every fresh connection: it keeps the server name for
// notifications and refreshes the stored password of a subscribed server.
// Called with settingsMu held.
func (s *Service) rememberVisitLocked(server *clientsettings.RecentServer, name string) {
	if name != "" {
		server.Name = name
	}
	if !server.Subscribed {
		return
	}
	password := s.client.ConnectionPassword()
	if password == "" {
		return
	}
	if current, err := clientsettings.UnprotectSecret(server.WatchSecret); err == nil && current == password {
		return
	}
	if secret, err := clientsettings.ProtectSecret(password); err == nil {
		server.WatchSecret = secret
	}
}

// ConnectionChanged is called on every client state change, which includes
// each start and end of speech. Watchers depend only on the server the client
// is connected to, so anything else returns at once.
func (s *Service) ConnectionChanged() {
	current := s.client.CurrentServer()
	s.noticeMu.Lock()
	same := current == s.watchServer
	s.watchServer = current
	s.noticeMu.Unlock()
	if !same {
		s.SyncWatchers()
	}
}

// SyncWatchers keeps a background watcher on every subscribed server except
// the one the client is connected to. Cheap when nothing changed.
func (s *Service) SyncWatchers() {
	current := s.client.CurrentServer()
	s.settingsMu.RLock()
	name := s.displayName
	type watched struct{ address, secret string }
	var servers []watched
	for _, server := range s.recentServers {
		if server.Subscribed && server.Address != current {
			servers = append(servers, watched{server.Address, server.WatchSecret})
		}
	}
	s.settingsMu.RUnlock()
	sort.Slice(servers, func(i, j int) bool { return servers[i].address < servers[j].address })
	var key strings.Builder
	key.WriteString(name)
	for _, server := range servers {
		key.WriteString("\x00" + server.address + "\x00" + server.secret)
	}
	s.noticeMu.Lock()
	if s.watchKey == key.String() && s.watchSynced {
		s.noticeMu.Unlock()
		return
	}
	s.watchKey, s.watchSynced = key.String(), true
	s.watchSeq++
	seq := s.watchSeq
	s.noticeMu.Unlock()
	targets := make([]clientapp.WatchTarget, 0, len(servers))
	for _, server := range servers {
		password, err := clientsettings.UnprotectSecret(server.secret)
		if err != nil {
			log.Printf("watch password unavailable: server=%s error=%v", server.address, err)
		}
		targets = append(targets, clientapp.WatchTarget{Address: server.address, Password: password})
	}
	// Stopping a watcher waits for its socket, so apply off the caller's thread,
	// in order: a newer target set makes an older pending one pointless.
	go func() {
		s.watchApplyMu.Lock()
		defer s.watchApplyMu.Unlock()
		s.noticeMu.Lock()
		latest := s.watchSeq == seq
		s.noticeMu.Unlock()
		if latest {
			s.client.SetWatchTargets(name, targets)
		}
	}()
}

// SetActiveChat reports the conversation the window shows: "channel" or
// "direct" with its id, or an empty kind when no chat is on screen.
func (s *Service) SetActiveChat(kind, id string) {
	s.noticeMu.Lock()
	s.activeChat = ""
	if kind == "channel" || kind == "direct" {
		s.activeChat = kind + ":" + id
	}
	s.noticeMu.Unlock()
}

var noticeTextKeys = []string{"arrival", "arrivalBody", "attachment", "direct", "server", "many"}

// SetNoticeTexts receives the notification templates of the interface
// language, like SetHotkeyNotices. {name}, {server}, {channel} and {count}
// are replaced.
func (s *Service) SetNoticeTexts(texts map[string]string) {
	out := make(map[string]string, len(noticeTextKeys))
	for _, key := range noticeTextKeys {
		text := strings.TrimSpace(texts[key])
		if len(text) > maxHotkeyNoticeLength {
			text = text[:maxHotkeyNoticeLength]
			for !utf8.ValidString(text) {
				text = text[:len(text)-1]
			}
		}
		out[key] = text
	}
	s.noticeMu.Lock()
	s.noticeTexts = out
	s.noticeMu.Unlock()
}

func fillNotice(template string, values map[string]string) string {
	for key, value := range values {
		template = strings.ReplaceAll(template, "{"+key+"}", value)
	}
	return template
}

// handleNotice decides what a notice becomes. Arrivals need a subscription;
// a message in the conversation on screen only plays the sound, as the chat
// itself shows it. Muted notifications stay silent otherwise.
func (s *Service) handleNotice(server string, watcher bool, notice protocol.Notice) {
	s.settingsMu.RLock()
	muted := s.notificationsMuted
	serverLabel := server
	subscribed := false
	for _, entry := range s.recentServers {
		if entry.Address == server {
			subscribed = entry.Subscribed
			if entry.Alias != "" {
				serverLabel = entry.Alias
			} else if entry.Name != "" {
				serverLabel = entry.Name
			}
		}
	}
	s.settingsMu.RUnlock()
	s.noticeMu.Lock()
	window, texts, active := s.noticeWindow, s.noticeTexts, s.activeChat
	// The watcher and the main session overlap for a moment when the client
	// connects to a subscribed server, so one notice may come twice. A message
	// is its id; an arrival is the arriving session, so a new entry is new.
	id := strconv.FormatInt(notice.MessageID, 10)
	if notice.Kind == protocol.NoticeArrival {
		id = strconv.FormatUint(notice.SessionID, 10)
	}
	key := server + "|" + strconv.Itoa(int(notice.Kind)) + "|" + id
	// An arrival without a session id (an older server) cannot be told apart.
	duplicate := s.noticeSeen[key] && !(notice.Kind == protocol.NoticeArrival && notice.SessionID == 0)
	if !duplicate {
		if s.noticeSeen == nil {
			s.noticeSeen = make(map[string]bool)
		}
		if old := s.noticeRing[s.noticeNext]; old != "" {
			delete(s.noticeSeen, old)
		}
		s.noticeRing[s.noticeNext] = key
		s.noticeNext = (s.noticeNext + 1) % len(s.noticeRing)
		s.noticeSeen[key] = true
	}
	s.noticeMu.Unlock()
	result := "popup"
	// Arrivals are rare and are what people ask about; a busy subscription
	// would write a line per message, so messages log only a missing window.
	defer func() {
		if notice.Kind == protocol.NoticeArrival || result == "no-window" {
			log.Printf("notice: server=%s watcher=%t kind=%d user_id=%d session_id=%d message_id=%d result=%s", server, watcher, notice.Kind, notice.UserID, notice.SessionID, notice.MessageID, result)
		}
	}()
	if window == nil {
		result = "no-window"
		return
	}
	if duplicate {
		result = "duplicate"
		return
	}
	focused := window.focused != nil && window.focused()
	values := map[string]string{"name": notice.Name, "server": serverLabel, "channel": notice.ChannelName}
	switch notice.Kind {
	case protocol.NoticeArrival:
		// The window already shows who is on its own server.
		switch {
		case muted:
			result = "muted"
			return
		case !subscribed:
			result = "not-subscribed"
			return
		case focused && !watcher:
			result = "window-active"
			return
		}
		s.popup(window, apppopup.Message{Title: fillNotice(texts["arrival"], values), Body: fillNotice(texts["arrivalBody"], values)})
	case protocol.NoticeMessage:
		kind := "channel"
		if notice.Target.Kind == domain.ChatDirect {
			kind = "direct"
		}
		onScreen := focused && !watcher && active == kind+":"+strconv.FormatInt(notice.Target.ID, 10)
		if onScreen {
			result = "sound-only"
			s.playMessageCue()
			return
		}
		if muted {
			result = "muted"
			return
		}
		title := notice.Name
		if notice.Target.Kind == domain.ChatChannel && notice.ChannelName != "" {
			title += " · " + notice.ChannelName
		} else if text := fillNotice(texts["direct"], values); text != "" {
			title += " · " + text
		}
		if watcher {
			title += " · " + serverLabel
		}
		body := notice.Text
		if body == "" {
			body = texts["attachment"]
		}
		// A message while the previous popup is still up turns it into a
		// summary: the count, and the latest message as its line.
		now := time.Now()
		s.noticeMu.Lock()
		if now.Before(s.burstUntil) {
			s.burstCount++
		} else {
			s.burstCount = 1
		}
		s.burstUntil = now.Add(messageNoticeLifetime)
		count := s.burstCount
		s.noticeMu.Unlock()
		if count > 1 {
			result = "popup-summary"
			body = title + ": " + body
			title = fillNotice(texts["many"], map[string]string{"count": strconv.Itoa(count)})
			if watcher {
				title += " · " + serverLabel
			}
		}
		s.popup(window, apppopup.Message{Title: title, Body: body, Lifetime: messageNoticeLifetime, OnClick: window.open})
		s.playMessageCue()
	}
}

// playMessageCue plays the incoming message sound at most once per
// messageCueInterval, whatever the number of messages.
func (s *Service) playMessageCue() {
	now := time.Now()
	s.noticeMu.Lock()
	due := now.Sub(s.lastCue) >= messageCueInterval
	if due {
		s.lastCue = now
	}
	s.noticeMu.Unlock()
	if due {
		s.client.PlayMessageCue()
	}
}

func (s *Service) popup(window *noticeWindow, message apppopup.Message) {
	if window.show == nil || message.Title == "" {
		return
	}
	if err := window.show(message); err != nil {
		log.Printf("notification popup failed: error=%v", err)
	}
}

// WatchStates maps subscribed server addresses to their watcher state for the
// interface: "connecting", "active", "password", "account", "untrusted",
// "unsupported", "banned" or "unavailable".
func (s *Service) WatchStates() map[string]string {
	return s.client.WatchStates()
}
