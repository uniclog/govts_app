package clientsettings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsMissingRNNoiseSensitivityToFull(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	data := []byte(`{"version":1,"rnnoiseEnabled":true}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	settings, err := NewStore(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if settings.RNNoiseSensitivity != 1 {
		t.Fatalf("RNNoise sensitivity = %v, want 1", settings.RNNoiseSensitivity)
	}
	if settings.Theme != ThemeSystem {
		t.Fatalf("theme = %q, want system", settings.Theme)
	}
}

func TestThemeRoundTripAndNormalization(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store := NewStore(path)
	if err := store.Save(Settings{Theme: ThemeLight}); err != nil {
		t.Fatal(err)
	}
	settings, err := store.Load()
	if err != nil || settings.Theme != ThemeLight {
		t.Fatalf("loaded settings = %+v, err = %v", settings, err)
	}
	if got := NormalizeTheme("future-theme"); got != ThemeSystem {
		t.Fatalf("unknown theme normalized to %q", got)
	}
}
