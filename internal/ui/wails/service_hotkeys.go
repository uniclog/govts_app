package wailsui

import (
	"context"
	"errors"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"uniclog.io/sonoryx/internal/clientsettings"
	"uniclog.io/sonoryx/internal/hotkeys"
)

// Error codes; the interface translates them through its errors catalog.
var (
	errHotkeyActionUnknown = errors.New("hotkey-action-unknown")
	errHotkeyKeyInvalid    = errors.New("hotkey-key-invalid")
)

// HotkeyDTO is one Ctrl+Alt shortcut. Status is "off" while hotkeys are
// disabled, "active" while the shortcut is armed, or "failed".
type HotkeyDTO struct {
	Action string `json:"action"`
	Key    string `json:"key"`
	Status string `json:"status"`
}

// StartHotkeys arms the saved shortcuts when they are enabled. The listener
// is not created while they are off, so nothing is captured.
func StartHotkeys(s *Service) {
	s.settingsMu.RLock()
	enabled := s.hotkeysEnabled
	s.settingsMu.RUnlock()
	s.hotkeyMu.Lock()
	if s.hotkeys != nil {
		s.hotkeyMu.Unlock()
		s.applyHotkeys()
		return
	}
	if !enabled {
		s.hotkeyMu.Unlock()
		return
	}
	s.hotkeyMu.Unlock()
	manager, err := hotkeys.Start(s.runHotkey)
	if err != nil {
		log.Printf("start hotkeys failed: error=%v", err)
		return
	}
	s.hotkeyMu.Lock()
	if s.hotkeys != nil {
		s.hotkeyMu.Unlock()
		manager.Close()
		s.applyHotkeys()
		return
	}
	s.hotkeys = manager
	s.hotkeyMu.Unlock()
	s.applyHotkeys()
}

func StopHotkeys(s *Service) {
	s.hotkeyMu.Lock()
	manager := s.hotkeys
	s.hotkeys = nil
	s.hotkeyMu.Unlock()
	if manager != nil {
		manager.Close()
	}
}

func (s *Service) HotkeysEnabled() bool {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.hotkeysEnabled
}

func (s *Service) SetHotkeysEnabled(value bool) error {
	s.settingsMu.Lock()
	s.hotkeysEnabled = value
	s.settingsMu.Unlock()
	StartHotkeys(s)
	return s.saveSettings()
}

func (s *Service) Hotkeys() []HotkeyDTO {
	s.settingsMu.RLock()
	keys := cloneStringMap(s.hotkeyKeys)
	enabled := s.hotkeysEnabled
	s.settingsMu.RUnlock()
	s.hotkeyMu.Lock()
	failed := s.hotkeyErrors
	s.hotkeyMu.Unlock()
	result := make([]HotkeyDTO, 0, len(clientsettings.HotkeyActions))
	for _, action := range clientsettings.HotkeyActions {
		status := "active"
		if !enabled {
			status = "off"
		} else if failed[action] != nil {
			status = "failed"
		}
		result = append(result, HotkeyDTO{Action: action, Key: keys[action], Status: status})
	}
	return result
}

// SetHotkey changes the key pressed with Ctrl+Alt for one action. A key that
// belongs to another action swaps with it, so the two can exchange places
// without a spare key in between.
func (s *Service) SetHotkey(action, key string) error {
	if !clientsettings.KnownHotkeyAction(action) {
		return errHotkeyActionUnknown
	}
	normalized, ok := clientsettings.NormalizeHotkeyKey(key)
	if !ok {
		return errHotkeyKeyInvalid
	}
	s.settingsMu.Lock()
	current := s.hotkeyKeys[action]
	for other, otherKey := range s.hotkeyKeys {
		if other != action && otherKey == normalized {
			s.hotkeyKeys[other] = current
			break
		}
	}
	s.hotkeyKeys[action] = normalized
	s.settingsMu.Unlock()
	s.applyHotkeys()
	return s.saveSettings()
}

func (s *Service) applyHotkeys() {
	s.settingsMu.RLock()
	enabled := s.hotkeysEnabled
	var bindings []hotkeys.Binding
	if enabled {
		bindings = make([]hotkeys.Binding, 0, len(clientsettings.HotkeyActions))
		for _, action := range clientsettings.HotkeyActions {
			bindings = append(bindings, hotkeys.Binding{Action: action, Key: s.hotkeyKeys[action]})
		}
	}
	s.settingsMu.RUnlock()
	s.hotkeyMu.Lock()
	manager := s.hotkeys
	s.hotkeyMu.Unlock()
	if manager == nil {
		return
	}
	failed := manager.Apply(bindings)
	s.hotkeyMu.Lock()
	s.hotkeyErrors = failed
	s.hotkeyMu.Unlock()
	for action, err := range failed {
		log.Printf("arm hotkey failed: action=%s error=%v", action, err)
	}
}

// runHotkey does what the matching button does, cue included. The new state
// is read under the button's lock, not from a snapshot taken beforehand.
func (s *Service) runHotkey(action string) {
	switch action {
	case clientsettings.HotkeyMute:
		changed, muted := s.toggleMuted()
		if changed {
			s.notifyHotkey(action, cueFor(muted, clientsettings.CueMicOff, clientsettings.CueMicOn))
		}
	case clientsettings.HotkeyDeafen:
		changed, deafened, err := s.toggleDeafened()
		if err != nil {
			log.Printf("hotkey deafen failed: error=%v", err)
			return
		}
		if changed {
			s.notifyHotkey(action, cueFor(deafened, clientsettings.CueSpeakerOff, clientsettings.CueSpeakerOn))
		}
	case clientsettings.HotkeyAway:
		ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
		defer cancel()
		changed, away, err := s.client.ToggleAway(ctx)
		if err != nil {
			log.Printf("hotkey away failed: error=%v", err)
			return
		}
		if changed {
			s.notifyHotkey(action, cueFor(away, clientsettings.CueAway, clientsettings.CueReturned))
		}
	}
}

func cueFor(on bool, whenOn, whenOff string) string {
	if on {
		return whenOn
	}
	return whenOff
}

// hotkeyNotifier shows a system notification for a hotkey press made while
// the main window is out of sight. Window buttons and the tray never use it.
type hotkeyNotifier struct {
	hidden func() bool
	send   func(id, title, body string) error
}

// ConfigureHotkeyNotifications connects hotkey presses to system notifications.
// hidden reports whether the main window is minimised or in the tray and must
// not call into the window: shutdown runs on the main thread and waits for
// the hotkey handler.
func ConfigureHotkeyNotifications(s *Service, hidden func() bool, send func(id, title, body string) error) {
	s.hotkeyMu.Lock()
	s.hotkeyNotifier = &hotkeyNotifier{hidden: hidden, send: send}
	s.hotkeyMu.Unlock()
}

func (s *Service) HotkeyNotifications() bool {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.hotkeyNotifications
}

func (s *Service) SetHotkeyNotifications(value bool) error {
	s.settingsMu.Lock()
	s.hotkeyNotifications = value
	s.settingsMu.Unlock()
	return s.saveSettings()
}

// notifyHotkey reports the new state; cue names which state it is.
// Presses that land while a toast is still being delivered collapse into one
// toast carrying the latest state.
func (s *Service) notifyHotkey(action, cue string) {
	s.settingsMu.RLock()
	enabled := s.hotkeyNotifications
	key := s.hotkeyKeys[action]
	s.settingsMu.RUnlock()
	s.hotkeyMu.Lock()
	notifier := s.hotkeyNotifier
	notices := s.hotkeyNotices
	s.hotkeyMu.Unlock()
	if !enabled || notifier == nil || !notifier.hidden() {
		return
	}
	title := notices[cue]
	if title == "" {
		return
	}
	s.toastMu.Lock()
	s.toastID = "sonoryx-hotkey-" + action
	s.toastTitle = title
	s.toastBody = "Ctrl + Alt + " + key
	if s.toastSending {
		s.toastPending = true
		s.toastMu.Unlock()
		return
	}
	s.toastSending = true
	s.toastMu.Unlock()
	go s.drainToasts()
}

func (s *Service) drainToasts() {
	for {
		s.toastMu.Lock()
		id, title, body := s.toastID, s.toastTitle, s.toastBody
		s.toastPending = false
		s.toastMu.Unlock()
		s.hotkeyMu.Lock()
		notifier := s.hotkeyNotifier
		s.hotkeyMu.Unlock()
		if notifier != nil {
			if err := notifier.send(id, title, body); err != nil {
				log.Printf("hotkey notification failed: error=%v", err)
			}
		}
		// The Windows fallback can take a moment. Wait so a burst becomes one
		// more toast with the final state, not one toast per press.
		time.Sleep(500 * time.Millisecond)
		s.toastMu.Lock()
		if !s.toastPending {
			s.toastSending = false
			s.toastMu.Unlock()
			return
		}
		s.toastMu.Unlock()
	}
}

var hotkeyNoticeCues = []string{
	clientsettings.CueMicOff,
	clientsettings.CueMicOn,
	clientsettings.CueSpeakerOff,
	clientsettings.CueSpeakerOn,
	clientsettings.CueAway,
	clientsettings.CueReturned,
}

const maxHotkeyNoticeLength = 128

// SetHotkeyNotices receives notification titles, keyed by cue id, from the
// interface's current language catalog. Go keeps no texts of its own: a
// hidden window still gets a notification in the chosen language, and a new
// language needs no change here.
func (s *Service) SetHotkeyNotices(texts map[string]string) {
	notices := make(map[string]string, len(hotkeyNoticeCues))
	for _, cue := range hotkeyNoticeCues {
		text := strings.TrimSpace(texts[cue])
		if len(text) > maxHotkeyNoticeLength {
			text = text[:maxHotkeyNoticeLength]
			for !utf8.ValidString(text) {
				text = text[:len(text)-1]
			}
		}
		if text != "" {
			notices[cue] = text
		}
	}
	s.hotkeyMu.Lock()
	s.hotkeyNotices = notices
	s.hotkeyMu.Unlock()
}
