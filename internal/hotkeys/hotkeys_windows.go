//go:build windows

package hotkeys

import (
	"errors"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
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

	vkLControl = 0xA2
	vkRControl = 0xA3
	vkLMenu    = 0xA4
	vkRMenu    = 0xA5

	mapvkVKToVSC   = 0
	klfNoteLLShell = 0x00000080
	// hklUS is the US English layout. MapVirtualKeyEx uses it to name a
	// physical key when that layout was not loaded.
	hklUS = 0x04090409
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procSetWindowsHookExW   = user32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx = user32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx      = user32.NewProc("CallNextHookEx")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procPeekMessageW        = user32.NewProc("PeekMessageW")
	procPostThreadMessageW  = user32.NewProc("PostThreadMessageW")
	procGetAsyncKeyState    = user32.NewProc("GetAsyncKeyState")
	procMapVirtualKeyExW    = user32.NewProc("MapVirtualKeyExW")
	procLoadKeyboardLayoutW = user32.NewProc("LoadKeyboardLayoutW")
	procGetModuleHandleW    = kernel32.NewProc("GetModuleHandleW")

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

// windowsBackend matches physical keys from a low-level keyboard hook.
// RegisterHotKey cannot do this: it matches a virtual key, which follows the
// active layout, and it treats AltGr as Ctrl+Alt, swallowing the character.
type windowsBackend struct {
	thread   uint32
	requests chan applyRequest
	done     chan struct{}
	hook     uintptr
	layout   uintptr
	scans    map[uint32]string
	held     map[uint32]bool
	dispatch func(string)
}

func startBackend(dispatch func(action string)) (backend, error) {
	b := &windowsBackend{
		requests: make(chan applyRequest, 1),
		done:     make(chan struct{}),
		scans:    make(map[uint32]string),
		held:     make(map[uint32]bool),
		dispatch: dispatch,
	}
	ready := make(chan uint32)
	go b.run(ready)
	b.thread = <-ready
	return b, nil
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
	for {
		result, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(result) <= 0 {
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
}

func (b *windowsBackend) replace(bindings []Binding) map[string]error {
	clear(b.held)
	scans := make(map[uint32]string, len(bindings))
	failed := make(map[string]error)
	for _, binding := range bindings {
		scan, err := b.scanOf(binding.Key)
		if err != nil {
			failed[binding.Action] = err
			continue
		}
		if owner, taken := scans[scan]; taken {
			failed[binding.Action] = errors.New("hotkey key already used by " + owner)
			continue
		}
		scans[scan] = binding.Action
	}
	b.scans = scans
	if len(scans) == 0 {
		b.removeHook()
		return failed
	}
	if err := b.installHook(); err != nil {
		b.scans = map[uint32]string{}
		for _, binding := range bindings {
			if _, ok := failed[binding.Action]; !ok {
				failed[binding.Action] = err
			}
		}
	}
	return failed
}

func (b *windowsBackend) scanOf(key string) (uint32, error) {
	if len(key) != 1 {
		return 0, errors.New("invalid hotkey key")
	}
	scan, _, _ := procMapVirtualKeyExW.Call(uintptr(key[0]), mapvkVKToVSC, b.layout)
	if scan == 0 {
		return 0, errors.New("unknown hotkey key")
	}
	return uint32(scan) & 0xFF, nil
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
	return nil
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
	scan := kb.scanCode & 0xFF
	up := wParam == wmKeyUp || wParam == wmSysKeyUp
	if up {
		if !b.held[scan] {
			return false
		}
		delete(b.held, scan)
		return true
	}
	if wParam != wmKeyDown && wParam != wmSysKeyDown {
		return false
	}
	if b.held[scan] {
		return true
	}
	action, ok := b.chord(scan)
	if !ok {
		return false
	}
	b.held[scan] = true
	b.dispatch(action)
	return true
}

func (b *windowsBackend) chord(scan uint32) (string, bool) {
	action, ok := b.scans[scan]
	if !ok || keyDown(vkRMenu) || !keyDown(vkLMenu) {
		return "", false
	}
	if !keyDown(vkLControl) && !keyDown(vkRControl) {
		return "", false
	}
	return action, true
}

func keyDown(vk uintptr) bool {
	state, _, _ := procGetAsyncKeyState.Call(vk)
	return state&0x8000 != 0
}

func (b *windowsBackend) apply(bindings []Binding) map[string]error {
	reply := make(chan map[string]error, 1)
	b.requests <- applyRequest{bindings: bindings, reply: reply}
	if result, _, err := procPostThreadMessageW.Call(uintptr(b.thread), wmApply, 0, 0); result == 0 {
		<-b.requests
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
	procPostThreadMessageW.Call(uintptr(b.thread), wmQuit, 0, 0)
	<-b.done
}

func failAll(bindings []Binding, err error) map[string]error {
	failed := make(map[string]error, len(bindings))
	for _, binding := range bindings {
		failed[binding.Action] = err
	}
	return failed
}
