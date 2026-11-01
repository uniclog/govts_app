package wailsui

import (
	"path/filepath"
	"reflect"
	"testing"

	"uniclog.io/sonoryx/internal/clientapp"
)

const volumeTestServer = "example.test:9000"

func newVolumeTestService() *Service {
	s := NewService(clientapp.New(clientapp.Options{}))
	s.serverAddress = volumeTestServer
	s.rememberParticipantVolumes = true
	return s
}

func TestSavedVolumesAreSeparatePerServerAndAccount(t *testing.T) {
	s := newVolumeTestService()
	s.recordParticipantVolume(10, 20, 0.5)
	s.recordParticipantVolume(11, 20, 2)

	want := map[int64]map[int64]float32{10: {20: 0.5}, 11: {20: 2}}
	if got := s.savedVolumesForServer(volumeTestServer); !reflect.DeepEqual(got, want) {
		t.Fatalf("saved = %v, want %v", got, want)
	}
	if got := s.savedVolumesForServer("other.test:9000"); len(got) != 0 {
		t.Fatalf("another server got %v", got)
	}
}

func TestSavedVolumesKeepMuteAndDropDefault(t *testing.T) {
	s := newVolumeTestService()
	s.recordParticipantVolume(10, 20, 0)
	want := map[int64]map[int64]float32{10: {20: 0}}
	if got := s.savedVolumesForServer(volumeTestServer); !reflect.DeepEqual(got, want) {
		t.Fatalf("muted = %v, want %v", got, want)
	}

	s.recordParticipantVolume(10, 20, 1)
	if len(s.participantVolumes) != 0 {
		t.Fatalf("default volume stayed stored: %v", s.participantVolumes)
	}
}

func TestSavedVolumesIgnoreUnknownIdentity(t *testing.T) {
	s := newVolumeTestService()
	s.recordParticipantVolume(0, 20, 0.5)
	s.recordParticipantVolume(10, 0, 0.5)
	if len(s.participantVolumes) != 0 {
		t.Fatalf("unknown identity was stored: %v", s.participantVolumes)
	}
}

func TestVolumeToggleStopsApplyingAndSavingAndKeepsValues(t *testing.T) {
	s := newVolumeTestService()
	account := volumeTestServer + "|10"
	s.recordParticipantVolume(10, 20, 0.5)

	if err := s.SetRememberParticipantVolumes(false); err != nil {
		t.Fatal(err)
	}
	if got := s.savedVolumesForServer(volumeTestServer); len(got) != 0 {
		t.Fatalf("disabled setting still applies %v", got)
	}
	s.recordParticipantVolume(10, 20, 2)
	if got := s.participantVolumes[account]["20"]; got != 0.5 {
		t.Fatalf("disabled setting changed the value to %v", got)
	}

	if err := s.SetRememberParticipantVolumes(true); err != nil {
		t.Fatal(err)
	}
	want := map[int64]map[int64]float32{10: {20: 0.5}}
	if got := s.savedVolumesForServer(volumeTestServer); !reflect.DeepEqual(got, want) {
		t.Fatalf("enabled = %v, want %v", got, want)
	}
}

func TestParticipantVolumesSurviveRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Sonoryx", "settings.json")
	first := NewService(clientapp.New(clientapp.Options{}))
	if err := first.enableSettings(path); err != nil {
		t.Fatal(err)
	}
	if !first.RememberParticipantVolumes() {
		t.Fatal("remembering must be on without a settings file")
	}
	first.serverAddress = volumeTestServer
	first.recordParticipantVolume(10, 20, 0)
	FlushParticipantVolumes(first)

	second := NewService(clientapp.New(clientapp.Options{}))
	if err := second.enableSettings(path); err != nil {
		t.Fatal(err)
	}
	if !second.RememberParticipantVolumes() {
		t.Fatal("remembering must stay on after a restart")
	}
	want := map[int64]map[int64]float32{10: {20: 0}}
	if got := second.savedVolumesForServer(volumeTestServer); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored after restart = %v, want %v", got, want)
	}

	if err := second.SetRememberParticipantVolumes(false); err != nil {
		t.Fatal(err)
	}
	third := NewService(clientapp.New(clientapp.Options{}))
	if err := third.enableSettings(path); err != nil {
		t.Fatal(err)
	}
	if third.RememberParticipantVolumes() {
		t.Fatal("a disabled setting must stay disabled after a restart")
	}
}
