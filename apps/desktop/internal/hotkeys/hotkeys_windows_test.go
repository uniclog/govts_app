//go:build windows

package hotkeys

import (
	"errors"
	"reflect"
	"testing"
	"unsafe"
)

func TestMenuMaskAndRepeatHandling(t *testing.T) {
	for _, tc := range []struct {
		name     string
		key      uintptr
		modifier uint8
		mask     bool
	}{
		{"Win", vkLWin, modWin, true},
		{"Alt", vkLMenu, modAlt, true},
		{"Ctrl", vkLControl, modCtrl, false},
		{"plain", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var events []string
			b := &windowsBackend{
				scans:    map[uint32]map[uint8]string{0x32: {tc.modifier: "mute"}},
				held:     make(map[uint32]bool),
				keyState: func(vk uintptr) bool { return tc.key != 0 && vk == tc.key },
				maskMenu: func() error { events = append(events, "mask"); return nil },
				dispatch: func(action string) { events = append(events, action) },
			}
			key := &kbHook{scanCode: 0x32, vkCode: 'M'}
			if !b.handle(wmKeyDown, key) || !b.handle(wmKeyDown, key) || !b.handle(wmKeyUp, key) {
				t.Fatal("matched press, repeats and release must be consumed")
			}
			want := []string{"mute"}
			if tc.mask {
				want = []string{"mask", "mute"}
			}
			if !reflect.DeepEqual(events, want) {
				t.Fatalf("events = %v, want %v", events, want)
			}
		})
	}
}

func TestUnmatchedPressDoesNotFireAfterModifierRelease(t *testing.T) {
	shift := true
	fired := 0
	b := &windowsBackend{
		scans: map[uint32]map[uint8]string{0x32: {0: "mute"}}, held: make(map[uint32]bool),
		keyState: func(vk uintptr) bool { return shift && vk == vkLShift },
		dispatch: func(string) { fired++ },
		maskMenu: func() error { t.Fatal("unmatched press must not mask menus"); return nil },
	}
	key := &kbHook{scanCode: 0x32, vkCode: 'M'}
	if b.handle(wmKeyDown, key) {
		t.Fatal("Shift+M should pass through")
	}
	shift = false
	if b.handle(wmKeyDown, key) || b.handle(wmKeyUp, key) || fired != 0 {
		t.Fatal("repeat after releasing Shift triggered a hotkey")
	}
	if !b.handle(wmKeyDown, key) || fired != 1 {
		t.Fatal("fresh M press did not trigger")
	}
}

func TestUnboundKeySkipsModifierQueries(t *testing.T) {
	b := &windowsBackend{
		scans:    map[uint32]map[uint8]string{0x32: {0: "mute"}},
		held:     make(map[uint32]bool),
		keyState: func(uintptr) bool { t.Fatal("unbound key queried modifier state"); return false },
	}
	key := &kbHook{scanCode: 0x1E, vkCode: 'A'}
	if b.handle(wmKeyDown, key) || b.handle(wmKeyDown, key) || b.handle(wmKeyUp, key) {
		t.Fatal("unbound key should pass through")
	}
}

func TestMaskFailureReportingDoesNotBlockKeyboard(t *testing.T) {
	fired := 0
	b := &windowsBackend{
		scans:      map[uint32]map[uint8]string{0x32: {modAlt: "mute"}},
		held:       make(map[uint32]bool),
		keyState:   func(vk uintptr) bool { return vk == vkLMenu },
		maskMenu:   func() error { return errors.New("input denied") },
		maskErrors: make(chan error, 1),
		dispatch:   func(string) { fired++ },
	}
	key := &kbHook{scanCode: 0x32, vkCode: 'M'}
	for i := 0; i < 2; i++ {
		if !b.handle(wmKeyDown, key) || !b.handle(wmKeyUp, key) {
			t.Fatal("matched key was not consumed")
		}
	}
	if fired != 2 || len(b.maskErrors) != 1 {
		t.Fatal("full diagnostic queue prevented hotkey dispatch")
	}
}

func TestMenuMaskEventsPassThroughWithoutChangingHeldKeys(t *testing.T) {
	b := &windowsBackend{held: make(map[uint32]bool)}
	key := &kbHook{vkCode: menuMaskVK, flags: llkhfInjected, dwExtraInfo: menuMaskTag}
	if b.handle(wmKeyDown, key) || b.handle(wmKeyUp, key) || len(b.held) != 0 {
		t.Fatal("menu-mask events were intercepted")
	}
	wantSize := uintptr(28)
	if unsafe.Sizeof(uintptr(0)) == 8 {
		wantSize = 40
	}
	if unsafe.Sizeof(keyboardEventInput{}) != wantSize {
		t.Fatal("INPUT layout does not match Windows ABI")
	}
}
