//go:build darwin

package hotkeys

/*
#cgo LDFLAGS: -framework Carbon
#include "hotkeys_darwin.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"uniclog.io/sonoryx/internal/clientsettings"
)

// Carbon modifier masks (Events.h). The stored names stay platform-neutral:
// Ctrl is Control, Alt is Option and Win is Command on a Mac keyboard.
const (
	carbonCmd     = 0x0100
	carbonShift   = 0x0200
	carbonOption  = 0x0800
	carbonControl = 0x1000

	// registerTimeoutMS bounds a wait for the main thread, which does not
	// serve its queue before NSApplication runs or after it stops. A timed-out
	// registration is dropped, never applied later.
	registerTimeoutMS = 5000
)

// macKeyCodes maps the stored key to its ANSI virtual key code (kVK_*), the
// physical position used by every layout.
var macKeyCodes = map[string]uint32{
	"A": 0x00, "S": 0x01, "D": 0x02, "F": 0x03, "H": 0x04, "G": 0x05, "Z": 0x06, "X": 0x07,
	"C": 0x08, "V": 0x09, "B": 0x0B, "Q": 0x0C, "W": 0x0D, "E": 0x0E, "R": 0x0F, "Y": 0x10,
	"T": 0x11, "1": 0x12, "2": 0x13, "3": 0x14, "4": 0x15, "6": 0x16, "5": 0x17, "9": 0x19,
	"7": 0x1A, "8": 0x1C, "0": 0x1D, "O": 0x1F, "U": 0x20, "I": 0x22, "P": 0x23, "L": 0x25,
	"J": 0x26, "K": 0x28, "N": 0x2D, "M": 0x2E,
	"F1": 0x7A, "F2": 0x78, "F3": 0x63, "F4": 0x76, "F5": 0x60, "F6": 0x61,
	"F7": 0x62, "F8": 0x64, "F9": 0x65, "F10": 0x6D, "F11": 0x67, "F12": 0x6F,
}

// darwinBackend registers Carbon hot keys. Presses arrive on the main thread
// through srxHotkeyPressed, which routes them by pressAction to pressTarget.
type darwinBackend struct{}

var (
	pressMu     sync.Mutex
	pressAction map[uint32]string // hot key id → action; nil when closed
	pressTarget func(string)
)

func startBackend(dispatch func(action string)) (backend, error) {
	pressMu.Lock()
	pressTarget, pressAction = dispatch, map[uint32]string{}
	pressMu.Unlock()
	return &darwinBackend{}, nil
}

//export srxHotkeyPressed
func srxHotkeyPressed(id C.uint32_t) {
	pressMu.Lock()
	action, target := pressAction[uint32(id)], pressTarget
	pressMu.Unlock()
	if action != "" && target != nil {
		target(action)
	}
}

func chordOf(value string) (code, modifiers uint32, err error) {
	normalized, ok := clientsettings.NormalizeHotkeyKey(value)
	if !ok {
		return 0, 0, errors.New("invalid hotkey key")
	}
	parts := strings.Split(normalized, "+")
	for _, modifier := range parts[:len(parts)-1] {
		switch modifier {
		case "Ctrl":
			modifiers |= carbonControl
		case "Alt":
			modifiers |= carbonOption
		case "Shift":
			modifiers |= carbonShift
		case "Win":
			modifiers |= carbonCmd
		}
	}
	key := parts[len(parts)-1]
	code, ok = macKeyCodes[key]
	if !ok {
		return 0, 0, errors.New("unknown hotkey key")
	}
	if macReserved(key, modifiers) {
		return 0, 0, errors.New("hotkey is reserved by macOS menus")
	}
	return code, modifiers, nil
}

// macReserved reports Command chords every Mac app's menus use: Quit,
// Close, Hide and Minimize, plus Quit/Close All with Shift. A global hot key
// would take them from every other app. Matches hotkeyLabels.ts.
func macReserved(key string, modifiers uint32) bool {
	switch modifiers {
	case carbonCmd:
		return key == "Q" || key == "W" || key == "H" || key == "M"
	case carbonCmd | carbonShift:
		return key == "Q" || key == "W"
	}
	return false
}

func (b *darwinBackend) apply(bindings []Binding) map[string]error {
	failed := make(map[string]error)
	var codes, modifiers [C.SRX_MAX_HOTKEYS]C.uint32_t
	var status [C.SRX_MAX_HOTKEYS]C.int32_t
	actions := make([]string, 0, len(bindings))
	seen := make(map[[2]uint32]string)
	for _, binding := range bindings {
		code, mods, err := chordOf(binding.Key)
		if err != nil {
			failed[binding.Action] = err
			continue
		}
		if owner, taken := seen[[2]uint32{code, mods}]; taken {
			failed[binding.Action] = errors.New("hotkey key already used by " + owner)
			continue
		}
		if len(actions) == C.SRX_MAX_HOTKEYS {
			failed[binding.Action] = errors.New("too many hotkeys")
			continue
		}
		seen[[2]uint32{code, mods}] = binding.Action
		codes[len(actions)], modifiers[len(actions)] = C.uint32_t(code), C.uint32_t(mods)
		actions = append(actions, binding.Action)
	}
	// Ids are slot+1; map them before registering so a press right after
	// registration already finds its action.
	pressMu.Lock()
	previous := pressAction
	if pressAction != nil {
		pressAction = make(map[uint32]string, len(actions))
		for i, action := range actions {
			pressAction[uint32(i+1)] = action
		}
	}
	pressMu.Unlock()
	if result := C.srxHotkeysApply(&codes[0], &modifiers[0], C.int(len(actions)), &status[0], registerTimeoutMS); result != 0 {
		// Nothing was registered and the job will not run later: the
		// previous keys are still armed, so they keep their previous routing.
		pressMu.Lock()
		if pressAction != nil {
			pressAction = previous
		}
		pressMu.Unlock()
		for _, action := range actions {
			failed[action] = fmt.Errorf("register hotkeys: main thread unavailable (%d)", int(result))
		}
		return failed
	}
	for i, action := range actions {
		if status[i] != 0 {
			failed[action] = fmt.Errorf("RegisterEventHotKey: OSStatus %d", int(status[i]))
			pressMu.Lock()
			if pressAction != nil {
				delete(pressAction, uint32(i+1))
			}
			pressMu.Unlock()
		}
	}
	return failed
}

// close stops dispatching first, so no press reaches Manager after it
// returns, then releases the keys for other programs.
func (b *darwinBackend) close() {
	pressMu.Lock()
	pressAction, pressTarget = nil, nil
	pressMu.Unlock()
	C.srxHotkeysApply(nil, nil, 0, nil, registerTimeoutMS)
}
