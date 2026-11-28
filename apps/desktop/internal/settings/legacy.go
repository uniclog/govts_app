package clientsettings

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// legacyProfileFiles are the preferences and identity that clients released
// before the Sonoryx rename keep in the "Govts" profile directory.
var legacyProfileFiles = []string{"settings.json", "client.seed", "voice-pins.json"}

// MigrateLegacyProfile copies those files into the Sonoryx profile when it
// does not have them yet, so an in-place update keeps the user's identity,
// trusted servers and settings. The legacy directory is left untouched.
func MigrateLegacyProfile(configDir string) error {
	legacy := filepath.Join(configDir, "Govts")
	current := filepath.Join(configDir, "Sonoryx")
	var migrateErrors []error
	for _, name := range legacyProfileFiles {
		target := filepath.Join(current, name)
		if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(legacy, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err == nil {
			err = writeNewFile(target, data)
		}
		if err != nil {
			migrateErrors = append(migrateErrors, fmt.Errorf("migrate %s: %w", name, err))
		}
	}
	return errors.Join(migrateErrors...)
}

func writeNewFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
	}
	return err
}
