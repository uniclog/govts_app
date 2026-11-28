//go:build windows

package nativeaudio

import (
	"runtime"
	"syscall"

	"golang.org/x/sys/windows"
)

// withAudioThread keeps COM initialized on one OS thread for the whole
// miniaudio context and device startup. WASAPI GetDevice fails with a generic
// error when CoUninitialize runs before the endpoint is activated, which is
// what happens for an explicit device id such as Remote Desktop audio.
func withAudioThread(fn func()) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	release := initCOM()
	defer release()
	fn()
}

func initCOM() func() {
	err := windows.CoInitializeEx(0, windows.COINIT_MULTITHREADED)
	// S_FALSE means this thread was already initialized and the refcount grew.
	if err == nil || err == syscall.Errno(1) {
		return windows.CoUninitialize
	}
	return func() {}
}
