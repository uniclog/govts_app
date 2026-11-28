package wailsui

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMessagePopupShowsOnlyWhileWindowIsInactive(t *testing.T) {
	s := NewService(nil)
	var shown [][2]string
	ConfigureMessagePopups(s, func(title, body string) error {
		shown = append(shown, [2]string{title, body})
		return nil
	})

	s.showMessagePopup("bob · General", "hello")
	if len(shown) != 0 {
		t.Fatalf("active window got popups %v", shown)
	}

	s.SetHotkeyWindowInactive(true)
	s.showMessagePopup("bob · General", "hello\n  there")
	s.showMessagePopup("", "no title")
	if len(shown) != 1 || shown[0] != [2]string{"bob · General", "hello there"} {
		t.Fatalf("inactive window popups = %v", shown)
	}

	s.SetHotkeyWindowInactive(false)
	s.showMessagePopup("bob · General", "again")
	if len(shown) != 1 {
		t.Fatalf("focused window got popups %v", shown)
	}
}

func TestMessagePopupWithoutWindowOrWithFailedSendIsHarmless(t *testing.T) {
	s := NewService(nil)
	s.SetHotkeyWindowInactive(true)
	s.showMessagePopup("bob", "hello") // No popup window configured yet.
	ConfigureMessagePopups(s, func(string, string) error { return errors.New("closed") })
	s.showMessagePopup("bob", "hello")
}

func TestClipPopupText(t *testing.T) {
	if got := clipPopupText(" a\r\n\tb  c ", 10); got != "a b c" {
		t.Fatalf("whitespace = %q", got)
	}
	exact := strings.Repeat("я", 10)
	if got := clipPopupText(exact, 10); got != exact {
		t.Fatalf("exact = %q", got)
	}
	got := clipPopupText(strings.Repeat("😀", 20), 10)
	if utf8.RuneCountInString(got) != 10 || !strings.HasSuffix(got, "…") || !utf8.ValidString(got) {
		t.Fatalf("clipped = %q", got)
	}
}
