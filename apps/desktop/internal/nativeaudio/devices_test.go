package nativeaudio

import (
	"testing"
	"unsafe"
)

func TestConfigureDeviceID(t *testing.T) {
	t.Parallel()

	var pointer unsafe.Pointer
	release, err := configureDeviceID(&pointer, "0102ff")
	if err != nil {
		t.Fatal(err)
	}
	if pointer == nil {
		t.Fatal("decoded device ID has a nil C pointer")
	}
	release()
	if pointer != nil {
		t.Fatal("release did not clear the device ID pointer")
	}

	pointer = nil
	if _, err := configureDeviceID(&pointer, "not-hex"); err == nil {
		t.Fatal("invalid device ID was accepted")
	}
	if pointer != nil {
		t.Fatal("invalid device ID changed target pointer")
	}
}

func TestConfigureDefaultDevice(t *testing.T) {
	t.Parallel()

	var pointer unsafe.Pointer
	release, err := configureDeviceID(&pointer, "")
	if err != nil {
		t.Fatal(err)
	}
	release()
	if pointer != nil {
		t.Fatal("default device must keep a nil device pointer")
	}
}
