package wailsui

import (
	"os"
	"path/filepath"
	"testing"

	"uniclog.io/sonoryx/internal/clientapp"
	"uniclog.io/sonoryx/internal/clientsettings"
)

func TestServicePersistsAndRestoresAudioSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Sonoryx", "settings.json")
	firstClient := clientapp.New(clientapp.Options{})
	first := NewService(firstClient)
	if err := first.enableSettings(path); err != nil {
		t.Fatal(err)
	}
	if err := first.setDisplayName("  Alice  "); err != nil {
		t.Fatal(err)
	}
	if err := first.SetDeafened(true); err != nil {
		t.Fatal(err)
	}
	if err := first.SetRNNoiseEnabled(false); err != nil {
		t.Fatal(err)
	}
	if err := first.SetRNNoiseSensitivity(0.64); err != nil {
		t.Fatal(err)
	}
	if err := first.SetVADMode("level"); err != nil {
		t.Fatal(err)
	}
	if err := first.SetVADSensitivity(0.73); err != nil {
		t.Fatal(err)
	}
	if err := first.SetVADEnabled(true); err != nil {
		t.Fatal(err)
	}
	if err := first.SetTheme(clientsettings.ThemeLight); err != nil {
		t.Fatal(err)
	}

	secondClient := clientapp.New(clientapp.Options{})
	second := NewService(secondClient)
	if err := second.enableSettings(path); err != nil {
		t.Fatal(err)
	}
	view := secondClient.Snapshot()
	if !view.Deafened || view.RNNoiseEnabled || view.RNNoiseSensitivity != 0.64 || !view.VADEnabled || view.VADMode != "level" || view.VADSensitivity != 0.73 {
		t.Fatalf("restored settings = %+v", view)
	}
	if got := second.SavedDisplayName(); got != "Alice" {
		t.Fatalf("restored display name = %q", got)
	}
	if got := second.Theme(); got != clientsettings.ThemeLight {
		t.Fatalf("restored theme = %q", got)
	}
}

func TestSettingsCanRecoverAfterMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "Sonoryx", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(clientapp.New(clientapp.Options{}))
	if err := service.enableSettings(path); err == nil {
		t.Fatal("malformed settings file was accepted")
	}
	if err := service.SetRNNoiseEnabled(false); err != nil {
		t.Fatalf("replace malformed settings: %v", err)
	}
	if _, err := clientsettings.NewStore(path).Load(); err != nil {
		t.Fatalf("saved replacement settings: %v", err)
	}
}
