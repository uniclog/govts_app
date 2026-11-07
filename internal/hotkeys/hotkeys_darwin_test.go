//go:build darwin

package hotkeys

import (
	"strconv"
	"testing"

	"uniclog.io/sonoryx/internal/clientsettings"
)

func TestChordOfMapsModifiersToCarbonMasks(t *testing.T) {
	tests := []struct {
		key       string
		code      uint32
		modifiers uint32
	}{
		{"Ctrl+Alt+M", 0x2E, carbonControl | carbonOption},
		{"ctrl+alt+d", 0x02, carbonControl | carbonOption},
		{"Win+Shift+E", 0x0E, carbonCmd | carbonShift},
		{"Win+Shift+M", 0x2E, carbonCmd | carbonShift},
		{"Ctrl+Win+Q", 0x0C, carbonControl | carbonCmd},
		{"F1", 0x7A, 0},
		{"Alt+F12", 0x6F, carbonOption},
		{"Ctrl+Alt+Shift+Win+0", 0x1D, carbonControl | carbonOption | carbonShift | carbonCmd},
	}
	for _, test := range tests {
		code, modifiers, err := chordOf(test.key)
		if err != nil || code != test.code || modifiers != test.modifiers {
			t.Errorf("chordOf(%q) = %#x, %#x, %v; want %#x, %#x", test.key, code, modifiers, err, test.code, test.modifiers)
		}
	}
}

func TestChordOfRejectsInvalidKeys(t *testing.T) {
	for _, key := range []string{"", "Ctrl+", "Ctrl+Ctrl+M", "Cmd+M", "F13", "Ctrl+Space", "Ctrl+Alt+MM",
		"Win+Q", "Win+W", "Win+H", "Win+M", "Win+Shift+Q", "Win+Shift+W"} {
		if _, _, err := chordOf(key); err == nil {
			t.Errorf("chordOf(%q) accepted an invalid key", key)
		}
	}
}

// Every key the settings accept must have a physical key code.
func TestEveryAcceptedKeyHasAMacKeyCode(t *testing.T) {
	var keys []string
	for c := 'A'; c <= 'Z'; c++ {
		keys = append(keys, string(c))
	}
	for c := '0'; c <= '9'; c++ {
		keys = append(keys, string(c))
	}
	for n := 1; n <= 12; n++ {
		keys = append(keys, "F"+strconv.Itoa(n))
	}
	codes := make(map[uint32]string)
	for _, key := range keys {
		if _, ok := clientsettings.NormalizeHotkeyKey(key); !ok {
			t.Fatalf("settings reject %q; update this test", key)
		}
		code, ok := macKeyCodes[key]
		if !ok {
			t.Errorf("no macOS key code for %q", key)
			continue
		}
		if other, taken := codes[code]; taken {
			t.Errorf("%q and %q share key code %#x", key, other, code)
		}
		codes[code] = key
	}
	if len(macKeyCodes) != len(keys) {
		t.Errorf("macKeyCodes has %d keys, settings accept %d", len(macKeyCodes), len(keys))
	}
}

func TestPressAfterCloseIsIgnored(t *testing.T) {
	pressed := make(chan string, 1)
	pressMu.Lock()
	pressTarget, pressAction = func(action string) { pressed <- action }, map[uint32]string{1: "mute"}
	pressMu.Unlock()
	srxHotkeyPressed(1)
	if got := <-pressed; got != "mute" {
		t.Fatalf("pressed %q, want mute", got)
	}
	srxHotkeyPressed(2) // unknown id
	pressMu.Lock()
	pressAction, pressTarget = nil, nil
	pressMu.Unlock()
	srxHotkeyPressed(1)
	select {
	case action := <-pressed:
		t.Fatalf("dispatched %q after close", action)
	default:
	}
}
