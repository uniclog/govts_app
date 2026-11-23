package wailsui

import (
	"context"
	"errors"
	"log"
	"maps"
	"strings"
	"time"
	"unicode/utf8"

	"uniclog.io/sonoryx/internal/clientsettings"
	"uniclog.io/sonoryx/internal/hotkeys"
)

// Error codes; the interface translates them through its errors catalog.
var (
	errHotkeyActionUnknown  = errors.New("hotkey-action-unknown")
	errHotkeyKeyInvalid     = errors.New("hotkey-key-invalid")
	errHotkeyCaptureExpired = errors.New("hotkey-capture-expired")
)

// Renewed by the recorder; a crashed or disconnected renderer cannot leave
// global hotkeys suspended indefinitely.
const hotkeyCaptureLease = 15 * time.Second

// HotkeyDTO is one complete shortcut. Status is "off" while hotkeys are
// disabled, "active" while the shortcut is armed, or "failed".
type HotkeyDTO struct {
	Action string `json:"action"`
	Key    string `json:"key"`
	Status string `json:"status"`
}

// StartHotkeys arms the saved shortcuts when they are enabled. The listener
// is not created while they are off, so nothing is captured.
func StartHotkeys(s *Service) {
	s.hotkeyLifecycleMu.Lock()
	defer s.hotkeyLifecycleMu.Unlock()
	s.settingsMu.RLock()
	enabled := s.hotkeysEnabled
	stopped := s.hotkeysStopped
	s.settingsMu.RUnlock()
	if stopped {
		return
	}
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
	s.hotkeys = manager
	s.hotkeyMu.Unlock()
	s.applyHotkeys()
}

func StopHotkeys(s *Service) {
	s.hotkeyLifecycleMu.Lock()
	defer s.hotkeyLifecycleMu.Unlock()
	s.settingsMu.Lock()
	s.hotkeysStopped = true
	s.clearHotkeyCaptureLocked()
	s.settingsMu.Unlock()
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

// SetHotkey changes the complete shortcut for one action. A shortcut that
// belongs to another action swaps with it, so the two can exchange places
// without a spare key in between.
func (s *Service) SetHotkey(action, key string) error {
	return s.setHotkey(action, key, 0)
}

// SetCapturedHotkey rejects a result from a cancelled or superseded recording.
func (s *Service) SetCapturedHotkey(capture uint32, action, key string) error {
	if capture == 0 {
		return errHotkeyCaptureExpired
	}
	return s.setHotkey(action, key, capture)
}

func (s *Service) setHotkey(action, key string, capture uint32) error {
	if !clientsettings.KnownHotkeyAction(action) {
		return errHotkeyActionUnknown
	}
	normalized, ok := clientsettings.NormalizeHotkeyKey(key)
	if !ok {
		return errHotkeyKeyInvalid
	}
	s.settingsMu.Lock()
	if capture != 0 && !s.hotkeyCaptureValidLocked(capture) {
		s.settingsMu.Unlock()
		return errHotkeyCaptureExpired
	}
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

// BeginHotkeyCapture acknowledges suspension before the interface records keys.
// Recording IDs keep late completions from resuming a newer recording.
func (s *Service) BeginHotkeyCapture() (uint32, error) {
	return s.beginHotkeyCapture(hotkeyCaptureLease)
}

func (s *Service) beginHotkeyCapture(lease time.Duration) (uint32, error) {
	s.settingsMu.Lock()
	if s.hotkeyWindowInactive || s.hotkeysStopped {
		s.settingsMu.Unlock()
		return 0, errHotkeyCaptureExpired
	}
	s.hotkeyCaptureSeq++
	if s.hotkeyCaptureSeq == 0 {
		s.hotkeyCaptureSeq++
	}
	capture := s.hotkeyCaptureSeq
	s.clearHotkeyCaptureLocked()
	s.hotkeyCapture = capture
	s.hotkeyCaptureDeadline = time.Now().Add(lease)
	s.hotkeyCaptureTimer = time.AfterFunc(lease, func() { s.expireHotkeyCapture(capture) })
	s.settingsMu.Unlock()
	s.applyHotkeys()
	s.settingsMu.RLock()
	current := s.hotkeyCaptureValidLocked(capture)
	s.settingsMu.RUnlock()
	if !current {
		return 0, errHotkeyCaptureExpired
	}
	return capture, nil
}

func (s *Service) EndHotkeyCapture(capture uint32) {
	s.settingsMu.Lock()
	if capture == 0 || s.hotkeyCapture != capture {
		s.settingsMu.Unlock()
		return
	}
	s.clearHotkeyCaptureLocked()
	s.settingsMu.Unlock()
	s.applyHotkeys()
}

func (s *Service) RenewHotkeyCapture(capture uint32) error {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	if !s.hotkeyCaptureValidLocked(capture) {
		return errHotkeyCaptureExpired
	}
	s.hotkeyCaptureDeadline = time.Now().Add(hotkeyCaptureLease)
	return nil
}

// Called with settingsMu held. Enforce the deadline at the API boundary too:
// timer scheduling can be delayed under load.
func (s *Service) hotkeyCaptureValidLocked(capture uint32) bool {
	return capture != 0 && s.hotkeyCapture == capture && !s.hotkeyWindowInactive &&
		!s.hotkeysStopped && time.Now().Before(s.hotkeyCaptureDeadline)
}

func (s *Service) clearHotkeyCaptureLocked() {
	s.hotkeyCapture = 0
	if s.hotkeyCaptureTimer != nil {
		s.hotkeyCaptureTimer.Stop()
		s.hotkeyCaptureTimer = nil
	}
}

func (s *Service) expireHotkeyCapture(capture uint32) {
	s.settingsMu.Lock()
	if s.hotkeyCapture != capture {
		s.settingsMu.Unlock()
		return
	}
	if remaining := time.Until(s.hotkeyCaptureDeadline); remaining > 0 {
		s.hotkeyCaptureTimer = time.AfterFunc(remaining, func() { s.expireHotkeyCapture(capture) })
		s.settingsMu.Unlock()
		return
	}
	s.clearHotkeyCaptureLocked()
	s.settingsMu.Unlock()
	s.applyHotkeys()
}

// SetHotkeyWindowInactive cancels recording when the window loses focus or
// becomes hidden, even when its WebView is idle.
// The native window calls this without waiting for JavaScript.
func (s *Service) SetHotkeyWindowInactive(inactive bool) {
	s.settingsMu.Lock()
	s.hotkeyWindowInactive = inactive
	cancelled := inactive && s.hotkeyCapture != 0
	if cancelled {
		s.clearHotkeyCaptureLocked()
	}
	s.settingsMu.Unlock()
	if cancelled {
		s.applyHotkeys()
	}
}

func (s *Service) applyHotkeys() {
	// Serialize snapshots as well as Apply, so a stale snapshot cannot win.
	s.hotkeyApplyMu.Lock()
	defer s.hotkeyApplyMu.Unlock()
	s.settingsMu.RLock()
	enabled := s.hotkeysEnabled && s.hotkeyCapture == 0
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
	case clientsettings.HotkeyNotifications:
		muted := s.ToggleNotificationsMuted()
		s.notifyHotkey(action, cueFor(muted, noticeNotificationsOff, noticeNotificationsOn))
	}
}

// Popup titles of the notification switch; they have no sound of their own.
const (
	noticeNotificationsOff = "notifications-off"
	noticeNotificationsOn  = "notifications-on"
)

func cueFor(on bool, whenOn, whenOff string) string {
	if on {
		return whenOn
	}
	return whenOff
}

// hotkeyNotifier shows an application popup for a hotkey press made while
// the main window is out of sight. Window buttons and the tray never use it.
type hotkeyNotifier struct {
	hidden func() bool
	send   func(title, body string) error
}

// ConfigureHotkeyNotifications connects hotkey presses to application popups.
// hidden reports whether the main window is minimised or in the tray and must
// not call into the window: shutdown runs on the main thread and waits for
// the hotkey handler.
// send must enqueue the popup without waiting for a UI thread, for the same reason.
func ConfigureHotkeyNotifications(s *Service, hidden func() bool, send func(title, body string) error) {
	s.hotkeyMu.Lock()
	s.hotkeyNotifier = &hotkeyNotifier{hidden: hidden, send: send}
	s.hotkeyMu.Unlock()
}

// SetPopupLook receives the #rrggbb card colours of the current interface
// theme. Go only keeps them for the next popup; apppopup.ParseLook validates.
func (s *Service) SetPopupLook(colors map[string]string) {
	s.hotkeyMu.Lock()
	s.popupLook = maps.Clone(colors)
	s.hotkeyMu.Unlock()
}

// PopupLook is a copy of the last palette pushed by the interface.
func (s *Service) PopupLook() map[string]string {
	s.hotkeyMu.Lock()
	defer s.hotkeyMu.Unlock()
	return maps.Clone(s.popupLook)
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
// The popup owns coalescing: a new press replaces its text and restarts its timer.
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
	if err := notifier.send(title, strings.ReplaceAll(key, "+", " + ")); err != nil {
		log.Printf("hotkey popup failed: error=%v", err)
	}
}

var hotkeyNoticeCues = []string{
	clientsettings.CueMicOff,
	clientsettings.CueMicOn,
	clientsettings.CueSpeakerOff,
	clientsettings.CueSpeakerOn,
	clientsettings.CueAway,
	clientsettings.CueReturned,
	noticeNotificationsOff,
	noticeNotificationsOn,
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
