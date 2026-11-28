//go:build windows

package hotkeys

import (
	"errors"
	"fmt"
	"log"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
	"uniclog.io/sonoryx/apps/desktop/internal/settings"
)

const (
	whKeyboardLL = 13

	wmQuit  = 0x0012
	wmApply = 0x8000 // WM_APP

	wmKeyDown    = 0x0100
	wmKeyUp      = 0x0101
	wmSysKeyDown = 0x0104
	wmSysKeyUp   = 0x0105

	pmNoRemove = 0
	pmRemove   = 1

	qsAllInput         = 0x04FF
	mwmoInputAvailable = 0x0004
	// A failed shutdown wake must not leave the listener waiting indefinitely.
	shutdownPollMS = 250

	vkLControl = 0xA2
	vkRControl = 0xA3
	vkLMenu    = 0xA4
	vkRMenu    = 0xA5
	vkLShift   = 0xA0
	vkRShift   = 0xA1
	vkLWin     = 0x5B
	vkRWin     = 0x5C

	mapvkVKToVSC   = 0
	mapvkVKToVSCEx = 4
	llkhfExtended  = 0x01
	llkhfInjected  = 0x10
	menuMaskVK     = 0xFF
	menuMaskTag    = 0x5352584D
	inputKeyboard  = 1
	keyeventfKeyUp = 0x0002
	klfNoteLLShell = 0x00000080
	// hklUS is the US English layout. MapVirtualKeyEx uses it to name a
	// physical key when that layout was not loaded.
	hklUS = 0x04090409
)

var (
	user32                          = windows.NewLazySystemDLL("user32.dll")
	kernel32                        = windows.NewLazySystemDLL("kernel32.dll")
	procSetWindowsHookExW           = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx         = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx              = user32.NewProc("CallNextHookEx")
	procMsgWaitForMultipleObjectsEx = user32.NewProc("MsgWaitForMultipleObjectsEx")
	procPeekMessageW                = user32.NewProc("PeekMessageW")
	procPostThreadMessageW          = user32.NewProc("PostThreadMessageW")
	procGetAsyncKeyState            = user32.NewProc("GetAsyncKeyState")
	procSendInput                   = user32.NewProc("SendInput")
	procGetForegroundWindow         = user32.NewProc("GetForegroundWindow")
	procGetWindowThreadProcessID    = user32.NewProc("GetWindowThreadProcessId")
	procGetKeyboardLayout           = user32.NewProc("GetKeyboardLayout")
	procMapVirtualKeyExW            = user32.NewProc("MapVirtualKeyExW")
	procLoadKeyboardLayoutW         = user32.NewProc("LoadKeyboardLayoutW")
	procGetModuleHandleW            = kernel32.NewProc("GetModuleHandleW")

	// hookProc stays allocated for the process lifetime: Windows calls it
	// whenever the hook is installed.
	hookProc = windows.NewCallback(lowLevelKeyboard)
	// hookState is read and written only on the listener thread.
	hookState *windowsBackend
)

type message struct {
	hwnd     uintptr
	message  uint32
	wParam   uintptr
	lParam   uintptr
	time     uint32
	x, y     int32
	lPrivate uint32
}

// kbHook matches KBDLLHOOKSTRUCT. scanCode is the physical key.
type kbHook struct {
	vkCode      uint32
	scanCode    uint32
	flags       uint32
	time        uint32
	dwExtraInfo uintptr
}

type applyRequest struct {
	bindings []Binding
	reply    chan map[string]error
}

type keyboardInput struct {
	vk, scan    uint16
	flags, time uint32
	extra       uintptr
}

// INPUT's union is MOUSEINPUT-sized, eight bytes larger than KEYBDINPUT on
// both 32-bit and 64-bit Windows. uintptr supplies the native alignment.
type keyboardEventInput struct {
	kind    uint32
	key     keyboardInput
	padding [8]byte
}

// Pass an inert, tagged key through to Windows before swallowing the primary
// key, so releasing Win/Alt does not look like a standalone modifier press.
func sendMenuMask() error {
	inputs := [2]keyboardEventInput{
		{kind: inputKeyboard, key: keyboardInput{vk: menuMaskVK, extra: menuMaskTag}},
		{kind: inputKeyboard, key: keyboardInput{vk: menuMaskVK, flags: keyeventfKeyUp, extra: menuMaskTag}},
	}
	sent, _, err := procSendInput.Call(uintptr(len(inputs)), uintptr(unsafe.Pointer(&inputs[0])), unsafe.Sizeof(inputs[0]))
	if sent != uintptr(len(inputs)) {
		return fmt.Errorf("menu mask input: sent %d of %d events: %v", sent, len(inputs), err)
	}
	return nil
}

type shortcut struct {
	scan      uint32
	modifiers uint8
}

const (
	modCtrl uint8 = 1 << iota
	modAlt
	modShift
	modWin
)

// windowsBackend matches physical keys from a low-level keyboard hook.
// RegisterHotKey follows the active layout and treats AltGr as Ctrl+Alt.
type windowsBackend struct {
	thread   uint32
	requests chan applyRequest
	done     chan struct{}
	stopping atomic.Bool
	hook     uintptr
	layout   uintptr
	// Index physical keys first, avoiding modifier syscalls for unrelated keys.
	scans map[uint32]map[uint8]string
	// held contains every pressed physical key; its value records consumption.
	held       map[uint32]bool
	dispatch   func(string)
	keyState   func(uintptr) bool
	maskMenu   func() error
	maskErrors chan error
}

func startBackend(dispatch func(action string)) (backend, error) {
	b := &windowsBackend{
		requests:   make(chan applyRequest, 1),
		done:       make(chan struct{}),
		scans:      make(map[uint32]map[uint8]string),
		held:       make(map[uint32]bool),
		dispatch:   dispatch,
		keyState:   keyDown,
		maskMenu:   sendMenuMask,
		maskErrors: make(chan error, 1),
	}
	ready := make(chan uint32)
	go b.run(ready)
	b.thread = <-ready
	go b.logMaskErrors()
	return b, nil
}

// Disk logging must never stall the low-level keyboard callback. Keep at most
// one pending diagnostic rather than accumulating errors or goroutines.
func (b *windowsBackend) logMaskErrors() {
	for {
		select {
		case err := <-b.maskErrors:
			log.Printf("hotkey menu mask failed: error=%v", err)
		case <-b.done:
			return
		}
	}
}

func (b *windowsBackend) run(ready chan<- uint32) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(b.done)
	defer b.removeHook()
	var msg message
	procPeekMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0, pmNoRemove)
	name, err := windows.UTF16PtrFromString("00000409")
	if err == nil {
		layout, _, _ := procLoadKeyboardLayoutW.Call(uintptr(unsafe.Pointer(name)), klfNoteLLShell)
		b.layout = layout
	}
	if b.layout == 0 {
		b.layout = hklUS
	}
	ready <- windows.GetCurrentThreadId()
	for !b.stopping.Load() {
		// Check shutdown between messages as well as idle waits, so a full
		// queue cannot starve cleanup after PostThreadMessageW fails.
		for !b.stopping.Load() {
			result, _, _ := procPeekMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0, pmRemove)
			if result == 0 {
				break
			}
			if msg.message == wmQuit {
				return
			}
			if msg.message != wmApply {
				continue
			}
			select {
			case request := <-b.requests:
				request.reply <- b.replace(request.bindings)
			default:
			}
		}
		if b.stopping.Load() {
			return
		}
		result, _, err := procMsgWaitForMultipleObjectsEx.Call(0, 0, shutdownPollMS, qsAllInput, mwmoInputAvailable)
		if uint32(result) == windows.WAIT_FAILED {
			log.Printf("hotkey message wait failed: error=%v", err)
			return
		}
	}
}

func (b *windowsBackend) replace(bindings []Binding) map[string]error {
	scans := make(map[uint32]map[uint8]string, len(bindings))
	failed := make(map[string]error)
	for _, binding := range bindings {
		scan, err := b.scanOf(binding.Key)
		if err != nil {
			failed[binding.Action] = err
			continue
		}
		actions := scans[scan.scan]
		if actions == nil {
			actions = make(map[uint8]string)
		}
		if owner, taken := actions[scan.modifiers]; taken {
			failed[binding.Action] = errors.New("hotkey key already used by " + owner)
			continue
		}
		actions[scan.modifiers] = binding.Action
		scans[scan.scan] = actions
	}
	b.scans = scans
	if len(scans) == 0 {
		b.removeHook()
		return failed
	}
	if err := b.installHook(); err != nil {
		b.scans = map[uint32]map[uint8]string{}
		for _, binding := range bindings {
			if _, ok := failed[binding.Action]; !ok {
				failed[binding.Action] = err
			}
		}
	}
	return failed
}

func (b *windowsBackend) scanOf(value string) (shortcut, error) {
	normalized, ok := clientsettings.NormalizeHotkeyKey(value)
	if !ok {
		return shortcut{}, errors.New("invalid hotkey key")
	}
	parts := strings.Split(normalized, "+")
	var chord shortcut
	for _, modifier := range parts[:len(parts)-1] {
		switch modifier {
		case "Ctrl":
			chord.modifiers |= modCtrl
		case "Alt":
			chord.modifiers |= modAlt
		case "Shift":
			chord.modifiers |= modShift
		case "Win":
			chord.modifiers |= modWin
		}
	}
	key := parts[len(parts)-1]
	vk := uintptr(key[0])
	if len(key) > 1 {
		number, _ := strconv.Atoi(key[1:])
		vk = uintptr(0x70 + number - 1)
	}
	scan, _, _ := procMapVirtualKeyExW.Call(vk, mapvkVKToVSC, b.layout)
	if scan == 0 {
		return shortcut{}, errors.New("unknown hotkey key")
	}
	chord.scan = uint32(scan) & 0xFF
	return chord, nil
}

func (b *windowsBackend) installHook() error {
	if b.hook != 0 {
		return nil
	}
	module, _, _ := procGetModuleHandleW.Call(0)
	hookState = b
	hook, _, err := procSetWindowsHookExW.Call(whKeyboardLL, hookProc, module, 0)
	if hook == 0 {
		hookState = nil
		return err
	}
	b.hook = hook
	b.seedHeldKeys()
	return nil
}

// seedHeldKeys prevents keys held before installation from firing on repeat.
// Use the foreground layout to translate asynchronous virtual-key state back
// to physical keys, rather than assuming the foreground program uses US keys.
func (b *windowsBackend) seedHeldKeys() {
	clear(b.held)
	window, _, _ := procGetForegroundWindow.Call()
	thread, _, _ := procGetWindowThreadProcessID.Call(window, 0)
	layout, _, _ := procGetKeyboardLayout.Call(thread)
	if layout == 0 {
		layout = b.layout
	}
	for vk := uintptr(0x08); vk < 0xFF; vk++ {
		if !keyDown(vk) {
			continue
		}
		scan, _, _ := procMapVirtualKeyExW.Call(vk, mapvkVKToVSCEx, layout)
		if scan == 0 {
			continue
		}
		physical := uint32(scan) & 0xFF
		if scan&0xFF00 != 0 {
			physical |= 0x100
		}
		b.held[physical] = false
	}
}

func (b *windowsBackend) removeHook() {
	if b.hook == 0 {
		return
	}
	procUnhookWindowsHookEx.Call(b.hook)
	b.hook = 0
	hookState = nil
}

func lowLevelKeyboard(nCode, wParam uintptr, lParam unsafe.Pointer) uintptr {
	if int32(nCode) >= 0 && hookState != nil && lParam != nil {
		if hookState.handle(wParam, (*kbHook)(lParam)) {
			return 1
		}
	}
	next, _, _ := procCallNextHookEx.Call(0, nCode, wParam, uintptr(lParam))
	return next
}

// handle reports whether the event was consumed. A consumed key does not
// reach the focused program. AltGr holds the right Alt, so it is never
// consumed and the character is typed as usual.
func (b *windowsBackend) handle(wParam uintptr, kb *kbHook) bool {
	if kb.vkCode == menuMaskVK && kb.dwExtraInfo == menuMaskTag && kb.flags&llkhfInjected != 0 {
		return false
	}
	scan := kb.scanCode & 0xFF
	if kb.flags&llkhfExtended != 0 {
		scan |= 0x100
	}
	up := wParam == wmKeyUp || wParam == wmSysKeyUp
	if up {
		consumed := b.held[scan]
		delete(b.held, scan)
		return consumed
	}
	if wParam != wmKeyDown && wParam != wmSysKeyDown {
		return false
	}
	if consumed, held := b.held[scan]; held {
		return consumed
	}
	b.held[scan] = false
	action, modifiers, ok := b.chord(scan)
	if !ok {
		return false
	}
	b.held[scan] = true
	if modifiers&(modAlt|modWin) != 0 {
		if err := b.maskMenu(); err != nil {
			select {
			case b.maskErrors <- err:
			default:
			}
		}
	}
	b.dispatch(action)
	return true
}

func (b *windowsBackend) chord(scan uint32) (string, uint8, bool) {
	actions := b.scans[scan]
	if len(actions) == 0 {
		return "", 0, false
	}
	if b.keyState(vkRMenu) {
		return "", 0, false
	}
	var modifiers uint8
	if b.keyState(vkLControl) || b.keyState(vkRControl) {
		modifiers |= modCtrl
	}
	if b.keyState(vkLMenu) {
		modifiers |= modAlt
	}
	if b.keyState(vkLShift) || b.keyState(vkRShift) {
		modifiers |= modShift
	}
	if b.keyState(vkLWin) || b.keyState(vkRWin) {
		modifiers |= modWin
	}
	action, ok := actions[modifiers]
	return action, modifiers, ok
}

func keyDown(vk uintptr) bool {
	state, _, _ := procGetAsyncKeyState.Call(vk)
	return state&0x8000 != 0
}

func (b *windowsBackend) apply(bindings []Binding) map[string]error {
	select {
	case <-b.done:
		return failAll(bindings, errors.New("hotkey listener stopped"))
	default:
	}
	reply := make(chan map[string]error, 1)
	select {
	case b.requests <- applyRequest{bindings: bindings, reply: reply}:
	case <-b.done:
		return failAll(bindings, errors.New("hotkey listener stopped"))
	}
	if result, _, err := procPostThreadMessageW.Call(uintptr(b.thread), wmApply, 0, 0); result == 0 {
		select {
		case <-b.requests:
		default:
		}
		return failAll(bindings, err)
	}
	select {
	case failed := <-reply:
		return failed
	case <-b.done:
		return failAll(bindings, errors.New("hotkey listener stopped"))
	}
}

func (b *windowsBackend) close() {
	b.stopping.Store(true)
	select {
	case <-b.done:
		return
	default:
	}
	if result, _, err := procPostThreadMessageW.Call(uintptr(b.thread), wmQuit, 0, 0); result == 0 {
		log.Printf("hotkey shutdown wake failed: error=%v", err)
	}
	// The stop flag is also checked on the listener's bounded idle wait.
	// Always wait for removeHook before allowing Manager to close actions.
	<-b.done
}

func failAll(bindings []Binding, err error) map[string]error {
	failed := make(map[string]error, len(bindings))
	for _, binding := range bindings {
		failed[binding.Action] = err
	}
	return failed
}
