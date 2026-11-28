package wailsui

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestHotkeyCaptureLeaseRestoresHotkeysWithoutRenderer(t *testing.T) {
	s := NewService(nil)
	s.hotkeysEnabled = true
	token, err := s.beginHotkeyCapture(20 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer s.EndHotkeyCapture(token)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	for {
		s.settingsMu.RLock()
		active := s.hotkeyCapture
		s.settingsMu.RUnlock()
		if active == 0 {
			break
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatal("recording did not expire without EndHotkeyCapture")
		}
	}
	if !s.HotkeysEnabled() {
		t.Fatal("expiry changed the saved enabled preference")
	}
	if !errors.Is(s.RenewHotkeyCapture(token), errHotkeyCaptureExpired) {
		t.Fatal("expired recording was renewed")
	}
}

func TestExpiredCaptureCannotBeRenewedOrSavedBeforeTimerRuns(t *testing.T) {
	s := NewService(nil)
	token, err := s.BeginHotkeyCapture()
	if err != nil {
		t.Fatal(err)
	}
	defer s.EndHotkeyCapture(token)
	s.settingsMu.Lock()
	s.hotkeyCaptureDeadline = time.Now().Add(-time.Second)
	s.settingsMu.Unlock()
	if !errors.Is(s.RenewHotkeyCapture(token), errHotkeyCaptureExpired) {
		t.Fatal("overdue capture was revived")
	}
	if !errors.Is(s.SetCapturedHotkey(token, "mute", "F1"), errHotkeyCaptureExpired) {
		t.Fatal("overdue capture was saved")
	}
	if s.hotkeyKeys["mute"] == "F1" {
		t.Fatal("expired save changed the binding")
	}
}

func TestHotkeysCannotRestartAfterShutdown(t *testing.T) {
	s := NewService(nil)
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Go(func() { StartHotkeys(s) })
		workers.Go(func() { StopHotkeys(s) })
	}
	workers.Wait()
	s.settingsMu.Lock()
	s.hotkeysEnabled = true
	s.settingsMu.Unlock()
	StartHotkeys(s)
	if s.hotkeys != nil {
		t.Fatal("late start created a listener after shutdown")
	}
	if _, err := s.BeginHotkeyCapture(); !errors.Is(err, errHotkeyCaptureExpired) {
		t.Fatal("recording began after shutdown")
	}
}

func TestRenewedCaptureSurvivesItsOriginalDeadline(t *testing.T) {
	s := NewService(nil)
	token, err := s.BeginHotkeyCapture()
	if err != nil {
		t.Fatal(err)
	}
	defer s.EndHotkeyCapture(token)
	s.settingsMu.Lock()
	s.hotkeyCaptureTimer.Stop()
	s.settingsMu.Unlock()
	if err := s.RenewHotkeyCapture(token); err != nil {
		t.Fatal(err)
	}
	// Simulate an already queued timeout callback after the renewal won the lock.
	s.expireHotkeyCapture(token)
	s.settingsMu.RLock()
	current := s.hotkeyCapture
	s.settingsMu.RUnlock()
	if current != token {
		t.Fatal("old timeout cancelled a renewed recording")
	}
}

func TestStaleCaptureCannotCancelOrSaveOverANewerRecording(t *testing.T) {
	s := NewService(nil)
	first, err := s.BeginHotkeyCapture()
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.BeginHotkeyCapture()
	if err != nil {
		t.Fatal(err)
	}
	defer s.EndHotkeyCapture(second)
	s.EndHotkeyCapture(first)
	s.expireHotkeyCapture(first)
	if err := s.SetCapturedHotkey(first, "mute", "F1"); !errors.Is(err, errHotkeyCaptureExpired) {
		t.Fatalf("stale save = %v", err)
	}
	if err := s.RenewHotkeyCapture(second); err != nil {
		t.Fatal("new recording was cancelled:", err)
	}
	if err := s.SetCapturedHotkey(second, "mute", "Shift+F12"); err != nil {
		t.Fatal(err)
	}
	if s.hotkeyKeys["mute"] != "Shift+F12" {
		t.Fatal("new recording was not saved")
	}
	s.SetHotkeyWindowInactive(true)
	if _, err := s.BeginHotkeyCapture(); !errors.Is(err, errHotkeyCaptureExpired) {
		t.Fatal("recording began in an inactive window")
	}
	if err := s.RenewHotkeyCapture(second); !errors.Is(err, errHotkeyCaptureExpired) {
		t.Fatal("inactive window kept a recording alive")
	}
}
