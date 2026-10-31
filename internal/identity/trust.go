package identity

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var trustMu sync.Mutex

// ServerKeyChangedError is returned only after the handshake signature has
// been verified by the caller. The keys stay private to the trust workflow.
type ServerKeyChangedError struct {
	Endpoint string
	Previous string
	NewKey   string
}

func (e *ServerKeyChangedError) Error() string {
	return fmt.Sprintf("voice server identity changed for %s", e.Endpoint)
}

func KeyFingerprint(encoded string) string {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(key) != ed25519.PublicKeySize {
		return ""
	}
	return PublicKeyFingerprint(ed25519.PublicKey(key))
}

func PublicKeyFingerprint(key ed25519.PublicKey) string {
	if len(key) != ed25519.PublicKeySize {
		return ""
	}
	sum := sha256.Sum256(key)
	return fmt.Sprintf("SHA256:%x", sum)
}

// CheckOrTrust pins the signing key on first contact with this voice endpoint.
func CheckOrTrust(path, endpoint string, publicKey ed25519.PublicKey) error {
	if len(publicKey) != ed25519.PublicKeySize || endpoint == "" || path == "" {
		return errors.New("invalid voice server identity")
	}
	trustMu.Lock()
	defer trustMu.Unlock()
	pins := make(map[string]string)
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &pins); err != nil {
			return fmt.Errorf("decode voice server pins: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(publicKey)
	if previous, known := pins[endpoint]; known {
		if previous != encoded {
			return &ServerKeyChangedError{Endpoint: endpoint, Previous: previous, NewKey: encoded}
		}
		return nil
	}
	pins[endpoint] = encoded
	return writePins(path, pins)
}

// ReplaceServerKey replaces only the exact pin shown for confirmation. It
// never removes the pin or trusts a different key on the next connection.
func ReplaceServerKey(path string, change ServerKeyChangedError) error {
	if path == "" || change.Endpoint == "" || KeyFingerprint(change.NewKey) == "" {
		return errors.New("invalid voice server identity")
	}
	trustMu.Lock()
	defer trustMu.Unlock()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var pins map[string]string
	if err := json.Unmarshal(data, &pins); err != nil {
		return fmt.Errorf("decode voice server pins: %w", err)
	}
	if previous, known := pins[change.Endpoint]; !known || previous != change.Previous {
		return errors.New("saved voice server identity changed; reconnect before confirming")
	}
	pins[change.Endpoint] = change.NewKey
	return writePins(path, pins)
}

func writePins(path string, pins map[string]string) error {
	data, err := json.MarshalIndent(pins, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".voice-pins-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
