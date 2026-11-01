package wailsui

import (
	"path/filepath"
	"reflect"
	"testing"

	"uniclog.io/sonoryx/internal/clientapp"
)

const (
	volumeTestServer = "example.test:9000"
	volumeTestLocal  = int64(10)
	volumeTestRemote = int64(20)
)

func newVolumeTestService(server string) *Service {
	s := NewService(nil)
	s.serverAddress = server
	s.rememberParticipantVolumes = true
	return s
}

// volumeTestView describes a connected local session with remote participants
// given as session id and user id pairs.
func volumeTestView(localUser, localSession string, remote ...[2]string) ClientViewDTO {
	view := ClientViewDTO{ConnectionStatus: "connected", SnapshotFresh: true, UserID: localUser, SessionID: localSession}
	view.Participants = append(view.Participants, ParticipantDTO{SessionID: localSession, UserID: localUser, Local: true})
	for _, participant := range remote {
		view.Participants = append(view.Participants, ParticipantDTO{SessionID: participant[0], UserID: participant[1]})
	}
	return view
}

func TestParticipantVolumeRestoresOncePerParticipantSession(t *testing.T) {
	s := newVolumeTestService(volumeTestServer)
	s.recordParticipantVolume(volumeTestLocal, 1, volumeTestRemote, 2, 0.5)

	// A new local session means the client has cleared its session volumes.
	view := volumeTestView("10", "100", [2]string{"200", "20"})
	want := []participantVolumeRestore{{sessionID: 200, volume: 0.5}}
	if got := s.pendingParticipantVolumes(view); !reflect.DeepEqual(got, want) {
		t.Fatalf("first restore = %+v, want %+v", got, want)
	}
	if got := s.pendingParticipantVolumes(view); len(got) != 0 {
		t.Fatalf("second restore = %+v, want none", got)
	}

	// The participant reconnects with a new session id and the same account.
	rejoined := volumeTestView("10", "100", [2]string{"201", "20"})
	want = []participantVolumeRestore{{sessionID: 201, volume: 0.5}}
	if got := s.pendingParticipantVolumes(rejoined); !reflect.DeepEqual(got, want) {
		t.Fatalf("rejoin restore = %+v, want %+v", got, want)
	}

	// The local client reconnects and the same participant session must apply again.
	reconnected := volumeTestView("10", "101", [2]string{"201", "20"})
	if got := s.pendingParticipantVolumes(reconnected); !reflect.DeepEqual(got, want) {
		t.Fatalf("reconnect restore = %+v, want %+v", got, want)
	}
}

func TestParticipantVolumeIsSeparatePerServerAndAccount(t *testing.T) {
	s := newVolumeTestService(volumeTestServer)
	s.recordParticipantVolume(volumeTestLocal, 1, volumeTestRemote, 2, 0.5)
	remote := [2]string{"200", "20"}

	if got := s.pendingParticipantVolumes(volumeTestView("11", "100", remote)); len(got) != 0 {
		t.Fatalf("another account restored %+v", got)
	}
	s.serverAddress = "other.test:9000"
	if got := s.pendingParticipantVolumes(volumeTestView("10", "101", remote)); len(got) != 0 {
		t.Fatalf("another server restored %+v", got)
	}
	s.serverAddress = volumeTestServer
	if got := s.pendingParticipantVolumes(volumeTestView("10", "102", remote)); len(got) != 1 {
		t.Fatalf("original account restored %+v, want one value", got)
	}
}

func TestParticipantVolumeKeepsMuteAndDropsDefault(t *testing.T) {
	s := newVolumeTestService(volumeTestServer)
	account := volumeTestServer + "|10"

	s.recordParticipantVolume(volumeTestLocal, 1, volumeTestRemote, 2, 0)
	if volume, ok := s.participantVolumes[account]["20"]; !ok || volume != 0 {
		t.Fatalf("muted value = %v, stored %v", volume, ok)
	}
	want := []participantVolumeRestore{{sessionID: 200, volume: 0}}
	if got := s.pendingParticipantVolumes(volumeTestView("10", "100", [2]string{"200", "20"})); !reflect.DeepEqual(got, want) {
		t.Fatalf("mute restore = %+v, want %+v", got, want)
	}

	s.recordParticipantVolume(volumeTestLocal, 1, volumeTestRemote, 2, 1)
	if _, ok := s.participantVolumes[account]; ok {
		t.Fatalf("default volume stayed stored: %+v", s.participantVolumes)
	}
}

func TestParticipantVolumeRestoreDoesNotReplaceManualChange(t *testing.T) {
	s := newVolumeTestService(volumeTestServer)
	s.participantVolumes = map[string]map[string]float32{volumeTestServer + "|10": {"20": 0.5}}

	s.recordParticipantVolume(volumeTestLocal, 100, volumeTestRemote, 200, 2)
	if got := s.pendingParticipantVolumes(volumeTestView("10", "100", [2]string{"200", "20"})); len(got) != 0 {
		t.Fatalf("restore replaced a manual change: %+v", got)
	}
}

func TestParticipantVolumeToggleStopsRestoreAndSaveAndKeepsValues(t *testing.T) {
	s := newVolumeTestService(volumeTestServer)
	account := volumeTestServer + "|10"
	s.recordParticipantVolume(volumeTestLocal, 1, volumeTestRemote, 2, 0.5)

	if err := s.SetRememberParticipantVolumes(false); err != nil {
		t.Fatal(err)
	}
	if got := s.pendingParticipantVolumes(volumeTestView("10", "100", [2]string{"200", "20"})); len(got) != 0 {
		t.Fatalf("disabled restore = %+v", got)
	}
	s.recordParticipantVolume(volumeTestLocal, 100, volumeTestRemote, 200, 2)
	if got := s.participantVolumes[account]["20"]; got != 0.5 {
		t.Fatalf("disabled save changed the value to %v", got)
	}

	if err := s.SetRememberParticipantVolumes(true); err != nil {
		t.Fatal(err)
	}
	want := []participantVolumeRestore{{sessionID: 201, volume: 0.5}}
	if got := s.pendingParticipantVolumes(volumeTestView("10", "100", [2]string{"201", "20"})); !reflect.DeepEqual(got, want) {
		t.Fatalf("enabled restore = %+v, want %+v", got, want)
	}
}

func TestParticipantVolumeIgnoresUnknownIdentityAndUnfreshView(t *testing.T) {
	s := newVolumeTestService(volumeTestServer)
	s.recordParticipantVolume(0, 1, volumeTestRemote, 2, 0.5)
	s.recordParticipantVolume(volumeTestLocal, 1, 0, 2, 0.5)
	if len(s.participantVolumes) != 0 {
		t.Fatalf("unknown identity was stored: %+v", s.participantVolumes)
	}

	s.recordParticipantVolume(volumeTestLocal, 1, volumeTestRemote, 2, 0.5)
	stale := volumeTestView("10", "100", [2]string{"200", "20"})
	stale.SnapshotFresh = false
	if got := s.pendingParticipantVolumes(stale); len(got) != 0 {
		t.Fatalf("stale view restored %+v", got)
	}

	// A participant whose account is not known yet stays pending.
	unknown := volumeTestView("10", "100", [2]string{"200", "0"})
	if got := s.pendingParticipantVolumes(unknown); len(got) != 0 {
		t.Fatalf("unknown participant restored %+v", got)
	}
	known := volumeTestView("10", "100", [2]string{"200", "20"})
	if got := s.pendingParticipantVolumes(known); len(got) != 1 {
		t.Fatalf("participant with a known account restored %+v, want one value", got)
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
	first.recordParticipantVolume(volumeTestLocal, 1, volumeTestRemote, 2, 0)
	FlushParticipantVolumes(first)

	second := NewService(clientapp.New(clientapp.Options{}))
	if err := second.enableSettings(path); err != nil {
		t.Fatal(err)
	}
	if !second.RememberParticipantVolumes() {
		t.Fatal("remembering must stay on after a restart")
	}
	second.serverAddress = volumeTestServer
	want := []participantVolumeRestore{{sessionID: 200, volume: 0}}
	if got := second.pendingParticipantVolumes(volumeTestView("10", "100", [2]string{"200", "20"})); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored after restart = %+v, want %+v", got, want)
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
