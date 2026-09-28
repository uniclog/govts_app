package audio

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type scriptedCapture struct {
	mu        sync.Mutex
	samples   []int16
	reads     int
	failAfter int
}

func (s *scriptedCapture) Read(dst []int16) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reads++
	if s.failAfter > 0 && s.reads > s.failAfter {
		return 0, errors.New("device removed")
	}
	return copy(dst, s.samples), nil
}

func (s *scriptedCapture) Close() error { return nil }

func TestSwitchableRecorderWaitsWithoutDevice(t *testing.T) {
	recorder, err := newSwitchableRecorder(CodecConfig{}, "missing", func(CodecConfig, string) (captureDevice, error) {
		return nil, errors.New("miniaudio: Unknown error")
	})
	if err == nil || recorder == nil || recorder.Available() {
		t.Fatalf("open = %v, available = %t", err, recorder != nil && recorder.Available())
	}
	opened := make(chan struct{}, 1)
	recorder.SetAvailabilityHandler(func(available bool) {
		if available {
			opened <- struct{}{}
		}
	})
	done := make(chan error, 1)
	go func() {
		_, readErr := recorder.Read(make([]int16, 4))
		done <- readErr
	}()
	select {
	case err := <-done:
		t.Fatalf("read returned before a device existed: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	recorder.open = func(CodecConfig, string) (captureDevice, error) {
		return &scriptedCapture{samples: []int16{1, 2, 3, 4}}, nil
	}
	if err := recorder.Switch("mic"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-opened:
	case <-time.After(time.Second):
		t.Fatal("opening a device did not report capture")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("read stayed blocked after the device opened")
	}
	if !recorder.Available() {
		t.Fatal("recorder stayed unavailable")
	}
}

func TestCaptureFailureReturnsToIdleUntilClose(t *testing.T) {
	device := &scriptedCapture{samples: []int16{9}, failAfter: 1}
	recorder, err := newSwitchableRecorder(CodecConfig{}, "mic", func(CodecConfig, string) (captureDevice, error) {
		return device, nil
	})
	if err != nil || !recorder.Available() {
		t.Fatal(err)
	}
	unavailable := make(chan struct{}, 1)
	recorder.SetAvailabilityHandler(func(available bool) {
		if !available {
			unavailable <- struct{}{}
		}
	})
	if _, err := recorder.Read(make([]int16, 1)); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, readErr := recorder.Read(make([]int16, 1))
		done <- readErr
	}()
	select {
	case <-unavailable:
	case <-time.After(time.Second):
		t.Fatal("capture loss was not reported")
	}
	select {
	case err := <-done:
		t.Fatalf("read returned after capture loss: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	if recorder.Available() {
		t.Fatal("recorder stayed available")
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, io.EOF) {
			t.Fatalf("close unblocked read with %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not unblock read")
	}
}
