package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A watcher connects with nobody to confirm a key: CheckPinned accepts only
// the pinned key and never writes a new pin.
func TestCheckPinnedNeverTrustsANewKey(t *testing.T) {
	pinsPath := filepath.Join(t.TempDir(), "pins.json")
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckPinned(pinsPath, "127.0.0.1:9000", key); !errors.Is(err, ErrServerNotPinned) {
		t.Fatalf("missing pin file: %v, want ErrServerNotPinned", err)
	}
	if _, err := os.Stat(pinsPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("CheckPinned created the pin file: %v", err)
	}
	if err := CheckOrTrust(pinsPath, "127.0.0.1:9000", key); err != nil {
		t.Fatal(err)
	}
	if err := CheckPinned(pinsPath, "127.0.0.1:9000", key); err != nil {
		t.Fatalf("pinned key: %v", err)
	}
	if err := CheckPinned(pinsPath, "127.0.0.1:9001", key); !errors.Is(err, ErrServerNotPinned) {
		t.Fatalf("other endpoint: %v, want ErrServerNotPinned", err)
	}
	var changed *ServerKeyChangedError
	if err := CheckPinned(pinsPath, "127.0.0.1:9000", other); !errors.As(err, &changed) {
		t.Fatalf("changed key: %v, want ServerKeyChangedError", err)
	}
	if err := CheckOrTrust(pinsPath, "127.0.0.1:9001", key); err != nil {
		t.Fatal(err)
	}
	if err := CheckPinned(pinsPath, "127.0.0.1:9001", key); err != nil {
		t.Fatalf("second pin: %v", err)
	}
	if err := CheckPinned(pinsPath, "", key); err == nil {
		t.Fatal("empty endpoint accepted")
	}
}
