package nativeaudio

import (
	"log"
	"testing"

	"uniclog.io/sonoryx/internal/audio"
)

type testAudioPipeline struct {
	previewPipeline
	active, connected, captureAvailable bool
}

func (s *testAudioPipeline) AudioConnectionState() (bool, bool, bool) {
	return s.active, s.connected, s.captureAvailable
}
func (s *testAudioPipeline) LogAudioDeviceChange() {}

func TestControllerDefaultsToSystemAudioDevices(t *testing.T) {
	controller := NewController(log.Default())
	t.Cleanup(controller.Close)
	selection := controller.DeviceSelection()
	if selection.CaptureID != "" || selection.PlaybackID != "" {
		t.Fatalf("default audio devices = %+v, want empty system-default IDs", selection)
	}
}

func TestControllerSelectsOnlyAvailableAudioDevices(t *testing.T) {
	app := NewController(log.Default())
	t.Cleanup(app.Close)
	status := &testAudioPipeline{captureAvailable: true}
	app.AttachPipeline(status)
	app.listDevices = func() (audio.DeviceList, error) {
		return audio.DeviceList{
			Capture:  []audio.DeviceInfo{{ID: "capture-1", Name: "Microphone"}},
			Playback: []audio.DeviceInfo{{ID: "playback-1", Name: "Speakers"}},
		}, nil
	}

	if err := app.SetCaptureDevice("capture-1"); err != nil {
		t.Fatal(err)
	}
	if err := app.SetPlaybackDevice("playback-1"); err != nil {
		t.Fatal(err)
	}
	if got := app.DeviceSelection(); got.CaptureID != "capture-1" || got.PlaybackID != "playback-1" {
		t.Fatalf("audio device selection = %+v", got)
	}
	if err := app.SetCaptureDevice("missing"); err == nil {
		t.Fatal("unavailable capture device was accepted")
	}
	if err := app.SetPlaybackDevice(""); err != nil {
		t.Fatal(err)
	}
	if got := app.DeviceSelection().PlaybackID; got != "" {
		t.Fatalf("default playback device ID = %q, want empty", got)
	}
}

func TestControllerHotSwitchesAudioDeviceWithoutChangingConnectionState(t *testing.T) {
	app := NewController(log.Default())
	t.Cleanup(app.Close)
	status := &testAudioPipeline{captureAvailable: true}
	app.AttachPipeline(status)
	app.listDevices = func() (audio.DeviceList, error) {
		return audio.DeviceList{Capture: []audio.DeviceInfo{{ID: "capture-1", Name: "Microphone"}}}, nil
	}
	status.active = true
	status.connected = true

	applied := make(chan DeviceSelection, 1)
	go func() {
		request := <-app.changes
		applied <- request.selection
		request.done <- nil
	}()

	if err := app.SetCaptureDevice("capture-1"); err != nil {
		t.Fatal(err)
	}
	selection := <-applied
	if selection.CaptureID != "capture-1" {
		t.Fatalf("applied capture device = %q", selection.CaptureID)
	}
	if _, connected, _ := status.AudioConnectionState(); !connected {
		t.Fatal("device switch changed connection status")
	}
	if got := app.DeviceSelection().CaptureID; got != "capture-1" {
		t.Fatalf("stored capture device = %q", got)
	}
}
