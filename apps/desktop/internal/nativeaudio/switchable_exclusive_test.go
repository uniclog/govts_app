package nativeaudio

import (
	"errors"
	"io"
	"sync"
	"testing"
	"time"
	"uniclog.io/sonoryx/internal/audio"
)

// endpoint models one physical microphone that admits a single exclusive
// stream, as WASAPI does after the exclusive capture fallback.
type endpoint struct {
	mu     sync.Mutex
	holder *exclusiveDevice
	opens  int
}

func (e *endpoint) open(sample int16) (captureDevice, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.opens++
	if e.holder != nil {
		return nil, errors.New("miniaudio: Device or resource busy")
	}
	device := &exclusiveDevice{endpoint: e, sample: sample, closed: make(chan struct{})}
	e.holder = device
	return device, nil
}

func (e *endpoint) held() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.holder != nil
}

type exclusiveDevice struct {
	endpoint  *endpoint
	sample    int16
	closeOnce sync.Once
	closed    chan struct{}
	block     bool
}

func (d *exclusiveDevice) Exclusive() bool { return true }

func (d *exclusiveDevice) Read(dst []int16) (int, error) {
	if d.block {
		// Like MalgoRecorder: Read waits for device data until Close.
		<-d.closed
		return 0, io.EOF
	}
	select {
	case <-d.closed:
		return 0, io.EOF
	default:
	}
	for i := range dst {
		dst[i] = d.sample
	}
	return len(dst), nil
}

func (d *exclusiveDevice) Close() error {
	d.closeOnce.Do(func() {
		close(d.closed)
		d.endpoint.mu.Lock()
		if d.endpoint.holder == d {
			d.endpoint.holder = nil
		}
		d.endpoint.mu.Unlock()
	})
	return nil
}

func readOne(t *testing.T, recorder *SwitchableRecorder) int16 {
	t.Helper()
	samples := make([]int16, 1)
	if _, err := recorder.Read(samples); err != nil {
		t.Fatal(err)
	}
	return samples[0]
}

func TestSwitchReleasesExclusiveDeviceForSameEndpoint(t *testing.T) {
	jack := &endpoint{}
	// "" (default) and "jack" resolve to the same physical microphone.
	open := func(_ audio.CodecConfig, id string) (captureDevice, error) {
		if id == "" {
			return jack.open(1)
		}
		return jack.open(2)
	}
	recorder, err := newSwitchableRecorder(audio.CodecConfig{}, "", open)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	var events []bool
	recorder.SetAvailabilityHandler(func(available bool) { events = append(events, available) })

	if err := recorder.Switch("jack"); err != nil {
		t.Fatalf("switch to the same exclusive endpoint failed: %v", err)
	}
	if got := readOne(t, recorder); got != 2 {
		t.Fatalf("read from sample %d, want the new device", got)
	}
	if recorder.deviceID != "jack" || jack.opens != 3 {
		t.Fatalf("device = %q, opens = %d", recorder.deviceID, jack.opens)
	}
	if len(events) != 1 || !events[0] {
		t.Fatalf("availability events = %v, want [true]", events)
	}
}

func TestSwitchRestoresExclusiveDeviceWhenNewOneFails(t *testing.T) {
	jack := &endpoint{}
	broken := errors.New("miniaudio: Invalid argument")
	open := func(_ audio.CodecConfig, id string) (captureDevice, error) {
		if id == "usb" {
			return nil, broken
		}
		return jack.open(1)
	}
	recorder, err := newSwitchableRecorder(audio.CodecConfig{}, "jack", open)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()

	err = recorder.Switch("usb")
	if !errors.Is(err, broken) {
		t.Fatalf("switch error = %v, want the open failure", err)
	}
	if !recorder.Available() || recorder.deviceID != "jack" || !jack.held() {
		t.Fatalf("previous device was not restored: available=%t device=%q held=%t", recorder.Available(), recorder.deviceID, jack.held())
	}
	if got := readOne(t, recorder); got != 1 {
		t.Fatalf("read sample %d from the restored device", got)
	}
}

func TestSwitchReportsUnavailableWhenRestoreFails(t *testing.T) {
	jack := &endpoint{}
	restoreFails := false
	open := func(_ audio.CodecConfig, id string) (captureDevice, error) {
		if id == "usb" || restoreFails {
			return nil, errors.New("miniaudio: Invalid argument")
		}
		return jack.open(1)
	}
	recorder, err := newSwitchableRecorder(audio.CodecConfig{}, "jack", open)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()
	unavailable := make(chan struct{}, 1)
	recorder.SetAvailabilityHandler(func(available bool) {
		if !available {
			unavailable <- struct{}{}
		}
	})
	restoreFails = true

	if err := recorder.Switch("usb"); err == nil {
		t.Fatal("switch succeeded without any device")
	}
	select {
	case <-unavailable:
	default:
		t.Fatal("capture loss was not reported")
	}
	if recorder.Available() {
		t.Fatal("recorder stayed available")
	}

	// A later retry of the same selection must open it again.
	restoreFails = false
	if err := recorder.Switch("jack"); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if !recorder.Available() {
		t.Fatal("retry did not restore capture")
	}
}

func TestSwitchKeepsSharedDeviceWhenOpenFails(t *testing.T) {
	shared := &scriptedCapture{samples: []int16{5}}
	broken := errors.New("miniaudio: Invalid argument")
	opens := 0
	open := func(_ audio.CodecConfig, id string) (captureDevice, error) {
		opens++
		if id == "usb" {
			return nil, broken
		}
		return shared, nil
	}
	recorder, err := newSwitchableRecorder(audio.CodecConfig{}, "mic", open)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()

	if err := recorder.Switch("usb"); !errors.Is(err, broken) {
		t.Fatalf("switch error = %v", err)
	}
	if opens != 2 || recorder.deviceID != "mic" {
		t.Fatalf("shared device was released: opens=%d device=%q", opens, recorder.deviceID)
	}
	if got := readOne(t, recorder); got != 5 {
		t.Fatalf("read sample %d", got)
	}
}

func TestBlockedReadMovesToDeviceOpenedAfterExclusiveRelease(t *testing.T) {
	jack := &endpoint{}
	open := func(_ audio.CodecConfig, id string) (captureDevice, error) {
		device, err := jack.open(map[string]int16{"": 1, "jack": 2}[id])
		if err == nil && id == "" {
			device.(*exclusiveDevice).block = true
		}
		return device, err
	}
	recorder, err := newSwitchableRecorder(audio.CodecConfig{}, "", open)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()

	done := make(chan int16, 1)
	go func() {
		samples := make([]int16, 1)
		if _, err := recorder.Read(samples); err != nil {
			done <- -1
			return
		}
		done <- samples[0]
	}()
	// Let Read enter the blocking device before switching.
	time.Sleep(20 * time.Millisecond)
	if err := recorder.Switch("jack"); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got != 2 {
			t.Fatalf("blocked read returned %d, want data from the new device", got)
		}
	case <-time.After(time.Second):
		t.Fatal("read stayed on the released device")
	}
	if !recorder.Available() {
		t.Fatal("releasing the exclusive device reported capture loss")
	}
}
