//go:build windows && (amd64 || arm64)

package nativeaudio

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WASAPI process loopback (ActivateAudioInterfaceAsync on the virtual
// "VAD\Process_Loopback" device). Microsoft documents it from Windows 10
// build 20348; it is known to work from 2004 (19041). Activation fails on
// older systems, which the caller reports as ErrLoopbackUnsupported.

var (
	mmdevapi                        = windows.NewLazySystemDLL("mmdevapi.dll")
	procActivateAudioInterfaceAsync = mmdevapi.NewProc("ActivateAudioInterfaceAsync")
	user32                          = windows.NewLazySystemDLL("user32.dll")
	procGetWindowTextW              = user32.NewProc("GetWindowTextW")
	procGetWindowLongPtrW           = user32.NewProc("GetWindowLongPtrW")
	procGetWindow                   = user32.NewProc("GetWindow")
	dwmapi                          = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmGetWindowAttribute       = dwmapi.NewProc("DwmGetWindowAttribute")
)

var (
	iidIUnknown            = windows.GUID{Data1: 0x00000000, Data2: 0x0000, Data3: 0x0000, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIAgileObject        = windows.GUID{Data1: 0x94ea2b94, Data2: 0xe9cc, Data3: 0x49e0, Data4: [8]byte{0xc0, 0xff, 0xee, 0x64, 0xca, 0x8f, 0x5b, 0x90}}
	iidCompletionHandler   = windows.GUID{Data1: 0x41D949AB, Data2: 0x9862, Data3: 0x444A, Data4: [8]byte{0x80, 0xF6, 0xC2, 0x61, 0x33, 0x4D, 0xA5, 0xEB}}
	iidIAudioClient        = windows.GUID{Data1: 0x1CB9AD4C, Data2: 0xDBFA, Data3: 0x4c32, Data4: [8]byte{0xB1, 0x78, 0xC2, 0xF5, 0x68, 0xA7, 0x03, 0xB2}}
	iidIAudioCaptureClient = windows.GUID{Data1: 0xC8ADBD64, Data2: 0xE71E, Data3: 0x48a0, Data4: [8]byte{0xA4, 0xDE, 0x18, 0x5C, 0x39, 0x5C, 0xD3, 0x17}}
)

const (
	audclntStreamFlagsLoopback      = 0x00020000
	audclntStreamFlagsEventCallback = 0x00040000
	audclntBufferFlagsSilent        = 0x2
	activationTypeProcessLoopback   = 1
	processLoopbackModeInclude      = 0
	processLoopbackModeExclude      = 1
	vtBlob                          = 65
	eNoInterface                    = 0x80004002
	loopbackBufferDuration          = 1_000_000 // 100 ms in 100-ns units
	activationTimeout               = 5 * time.Second
)

// IAudioClient, IAudioCaptureClient and IActivateAudioInterfaceAsyncOperation
// method indexes after IUnknown's QueryInterface, AddRef and Release.
const (
	comRelease               = 2
	operationGetResult       = 3
	clientInitialize         = 3
	clientStart              = 10
	clientStop               = 11
	clientSetEventHandle     = 13
	clientGetService         = 14
	captureGetBuffer         = 3
	captureReleaseBuffer     = 4
	captureGetNextPacketSize = 5
)

// comObject is a COM interface pointer; out parameters are written into
// *comObject variables so no uintptr is ever converted back to a pointer.
type comObject struct{ vtbl *[16]uintptr }

func (o *comObject) call(method int, args ...uintptr) int32 {
	all := append([]uintptr{uintptr(unsafe.Pointer(o))}, args...)
	result, _, _ := syscall.SyscallN(o.vtbl[method], all...)
	return int32(result)
}

func (o *comObject) release() {
	if o != nil {
		o.call(comRelease)
	}
}

type audioClientActivationParams struct {
	activationType  uint32
	targetProcessID uint32
	loopbackMode    uint32
}

// PROPVARIANT holding a BLOB, amd64/arm64 layout.
type propVariantBlob struct {
	vt       uint16
	reserved [3]uint16
	size     uint32
	_        uint32
	data     *byte
}

type waveFormatEx struct {
	formatTag      uint16
	channels       uint16
	samplesPerSec  uint32
	avgBytesPerSec uint32
	blockAlign     uint16
	bitsPerSample  uint16
	size           uint16
}

// activationHandler implements IActivateAudioInterfaceCompletionHandler and
// IAgileObject. Its reference counting is a no-op and every handler stays
// reachable for the process lifetime (activationHandlers): Windows may call
// Release after activation returns, and a callback must never receive a
// pointer to freed Go memory. Activations are rare (each share, one probe).
type activationHandler struct {
	vtbl *[4]uintptr
	done chan struct{}
	once sync.Once
}

var activationVtbl = [4]uintptr{
	syscall.NewCallback(handlerQueryInterface),
	syscall.NewCallback(handlerAddRef),
	syscall.NewCallback(handlerRelease),
	syscall.NewCallback(handlerActivateCompleted),
}

var (
	handlersMu         sync.Mutex
	activationHandlers []*activationHandler
)

func handlerQueryInterface(this *activationHandler, iid *windows.GUID, out *unsafe.Pointer) uintptr {
	if *iid == iidIUnknown || *iid == iidIAgileObject || *iid == iidCompletionHandler {
		*out = unsafe.Pointer(this)
		return 0
	}
	*out = nil
	return eNoInterface
}

func handlerAddRef(*activationHandler) uintptr  { return 1 }
func handlerRelease(*activationHandler) uintptr { return 1 }

func handlerActivateCompleted(this *activationHandler, _ uintptr) uintptr {
	this.once.Do(func() { close(this.done) })
	return 0
}

func failed(hr int32) bool { return hr < 0 }

// StartLoopback starts a capture thread and returns once the device runs.
func StartLoopback(target LoopbackTarget) (*LoopbackCapture, error) {
	if procActivateAudioInterfaceAsync.Find() != nil {
		return nil, ErrLoopbackUnsupported
	}
	capture := newLoopbackCapture()
	stop := make(chan struct{})
	started := make(chan error, 1)
	var stopOnce sync.Once
	var event windows.Handle
	var eventMu sync.Mutex
	capture.stop = func() {
		stopOnce.Do(func() {
			close(stop)
			eventMu.Lock()
			if event != 0 {
				_ = windows.SetEvent(event)
			}
			eventMu.Unlock()
		})
	}
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		// Deferred first, so Close returns only after the device and COM
		// are released.
		var result error
		defer func() { capture.finish(result) }()
		release := initCOM()
		defer release()
		client, reader, handle, err := openLoopback(target)
		if err != nil {
			result = err
			started <- err
			return
		}
		eventMu.Lock()
		event = handle
		eventMu.Unlock()
		defer func() {
			client.call(clientStop)
			reader.release()
			client.release()
			eventMu.Lock()
			_ = windows.CloseHandle(event)
			event = 0
			eventMu.Unlock()
		}()
		if hr := client.call(clientStart); failed(hr) {
			result = fmt.Errorf("start loopback capture: 0x%08x", uint32(hr))
			started <- result
			return
		}
		started <- nil
		result = runLoopback(reader, handle, stop, capture)
	}()
	if err := <-started; err != nil {
		return nil, err
	}
	return capture, nil
}

func openLoopback(target LoopbackTarget) (*comObject, *comObject, windows.Handle, error) {
	mode := uint32(processLoopbackModeInclude)
	if target.Exclude {
		mode = processLoopbackModeExclude
	}
	params := audioClientActivationParams{activationType: activationTypeProcessLoopback, targetProcessID: target.ProcessID, loopbackMode: mode}
	variant := propVariantBlob{vt: vtBlob, size: uint32(unsafe.Sizeof(params)), data: (*byte)(unsafe.Pointer(&params))}
	path, err := windows.UTF16PtrFromString(`VAD\Process_Loopback`)
	if err != nil {
		return nil, nil, 0, err
	}
	handler := &activationHandler{vtbl: &activationVtbl, done: make(chan struct{})}
	handlersMu.Lock()
	activationHandlers = append(activationHandlers, handler)
	handlersMu.Unlock()
	var operation *comObject
	hr, _, _ := procActivateAudioInterfaceAsync.Call(uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&iidIAudioClient)), uintptr(unsafe.Pointer(&variant)), uintptr(unsafe.Pointer(handler)), uintptr(unsafe.Pointer(&operation)))
	runtime.KeepAlive(params)
	if failed(int32(hr)) || operation == nil {
		return nil, nil, 0, fmt.Errorf("%w: activate 0x%08x", ErrLoopbackUnsupported, uint32(hr))
	}
	select {
	case <-handler.done:
	case <-time.After(activationTimeout):
		operation.release()
		return nil, nil, 0, fmt.Errorf("%w: activation timed out", ErrLoopbackUnsupported)
	}
	var activateResult int32
	var client *comObject
	hr2 := operation.call(operationGetResult, uintptr(unsafe.Pointer(&activateResult)), uintptr(unsafe.Pointer(&client)))
	operation.release()
	if failed(hr2) || failed(activateResult) || client == nil {
		client.release()
		return nil, nil, 0, fmt.Errorf("%w: activation result 0x%08x/0x%08x", ErrLoopbackUnsupported, uint32(hr2), uint32(activateResult))
	}
	format := waveFormatEx{formatTag: 1, channels: LoopbackChannels, samplesPerSec: LoopbackSampleRate, avgBytesPerSec: LoopbackSampleRate * loopbackFrameBytes, blockAlign: loopbackFrameBytes, bitsPerSample: 16}
	if hr := client.call(clientInitialize, 0, audclntStreamFlagsLoopback|audclntStreamFlagsEventCallback, loopbackBufferDuration, 0, uintptr(unsafe.Pointer(&format)), 0); failed(hr) {
		client.release()
		return nil, nil, 0, fmt.Errorf("initialize loopback capture: 0x%08x", uint32(hr))
	}
	var reader *comObject
	if hr := client.call(clientGetService, uintptr(unsafe.Pointer(&iidIAudioCaptureClient)), uintptr(unsafe.Pointer(&reader))); failed(hr) || reader == nil {
		client.release()
		return nil, nil, 0, fmt.Errorf("loopback capture client: 0x%08x", uint32(hr))
	}
	event, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		reader.release()
		client.release()
		return nil, nil, 0, err
	}
	if hr := client.call(clientSetEventHandle, uintptr(event)); failed(hr) {
		_ = windows.CloseHandle(event)
		reader.release()
		client.release()
		return nil, nil, 0, fmt.Errorf("loopback event: 0x%08x", uint32(hr))
	}
	return client, reader, event, nil
}

// runLoopback drains every packet on each event. A silent target produces
// no packets at all; the reader then simply gets nothing.
func runLoopback(reader *comObject, event windows.Handle, stop <-chan struct{}, capture *LoopbackCapture) error {
	silence := make([]byte, 0)
	for {
		select {
		case <-stop:
			return nil
		default:
		}
		if _, err := windows.WaitForSingleObject(event, 200); err != nil {
			return err
		}
		for {
			var frames uint32
			if hr := reader.call(captureGetNextPacketSize, uintptr(unsafe.Pointer(&frames))); failed(hr) {
				return fmt.Errorf("loopback packet size: 0x%08x", uint32(hr))
			}
			if frames == 0 {
				break
			}
			var data *byte
			var count, flags uint32
			if hr := reader.call(captureGetBuffer, uintptr(unsafe.Pointer(&data)), uintptr(unsafe.Pointer(&count)), uintptr(unsafe.Pointer(&flags)), 0, 0); failed(hr) {
				return fmt.Errorf("loopback buffer: 0x%08x", uint32(hr))
			}
			size := int(count) * loopbackFrameBytes
			if flags&audclntBufferFlagsSilent != 0 || data == nil {
				if cap(silence) < size {
					silence = make([]byte, size)
				}
				capture.push(silence[:size])
			} else if size > 0 {
				capture.push(unsafe.Slice(data, size))
			}
			if hr := reader.call(captureReleaseBuffer, uintptr(count)); failed(hr) {
				return fmt.Errorf("loopback release: 0x%08x", uint32(hr))
			}
		}
	}
}

const (
	gwlExStyle      = ^uintptr(19) // -20
	wsExToolWindow  = 0x00000080
	wsExAppWindow   = 0x00040000
	wsExNoActivate  = 0x08000000
	gwOwner         = 4
	dwmwaCloaked    = 14
	maxWindowTitle  = 256
	processNameSize = 1024
)

var (
	enumMu       sync.Mutex
	enumApps     []LoopbackApp
	enumSeen     map[uint32]bool
	enumSelf     uint32
	enumCallback = syscall.NewCallback(enumWindow)
)

func enumWindow(hwnd windows.HWND, _ uintptr) uintptr {
	if !windows.IsWindowVisible(hwnd) {
		return 1
	}
	if owner, _, _ := procGetWindow.Call(uintptr(hwnd), gwOwner); owner != 0 {
		return 1
	}
	// Tool windows, and notifications or toasts that never take focus, are not
	// apps; an explicit taskbar window (WS_EX_APPWINDOW) still is.
	if style, _, _ := procGetWindowLongPtrW.Call(uintptr(hwnd), gwlExStyle); style&wsExToolWindow != 0 || style&wsExNoActivate != 0 && style&wsExAppWindow == 0 {
		return 1
	}
	// Cloaked windows are hidden shell and UWP hosts, not apps the user sees.
	var cloaked uint32
	if hr, _, _ := procDwmGetWindowAttribute.Call(uintptr(hwnd), dwmwaCloaked, uintptr(unsafe.Pointer(&cloaked)), unsafe.Sizeof(cloaked)); hr == 0 && cloaked != 0 {
		return 1
	}
	var pid uint32
	if _, err := windows.GetWindowThreadProcessId(hwnd, &pid); err != nil || pid == 0 || pid == enumSelf || enumSeen[pid] {
		return 1
	}
	title := make([]uint16, maxWindowTitle)
	n, _, _ := procGetWindowTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(&title[0])), maxWindowTitle)
	if n == 0 {
		return 1
	}
	enumSeen[pid] = true
	enumApps = append(enumApps, LoopbackApp{ProcessID: pid, Name: processName(pid), Title: windows.UTF16ToString(title[:n])})
	return 1
}

func processName(pid uint32) string {
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(process)
	buffer := make([]uint16, processNameSize)
	size := uint32(len(buffer))
	if windows.QueryFullProcessImageName(process, 0, &buffer[0], &size) != nil {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(windows.UTF16ToString(buffer[:size])), ".exe")
}

// LoopbackApps lists processes with a visible, titled top-level window,
// except Sonoryx, one entry per process.
func LoopbackApps() ([]LoopbackApp, error) {
	enumMu.Lock()
	defer enumMu.Unlock()
	enumApps, enumSeen, enumSelf = nil, make(map[uint32]bool), currentProcessID()
	if err := windows.EnumWindows(enumCallback, nil); err != nil {
		return nil, err
	}
	apps := enumApps
	enumApps, enumSeen = nil, nil
	return apps, nil
}
