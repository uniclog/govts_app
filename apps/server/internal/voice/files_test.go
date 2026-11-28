package voice

import (
	"encoding/base64"
	"testing"
	"time"
)

func TestFileCredentialRotationOverlapExpiryAndSessionRevocation(t *testing.T) {
	h := NewHub()
	h.SetFileEndpoint(9001, "pin")
	h.Add(&Session{ID: 1, UserID: 1})
	old, err := h.FileAccess(1)
	if err != nil {
		t.Fatal(err)
	}
	oldBytes, err := base64.RawURLEncoding.DecodeString(old.Credential)
	if err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.sessions[1].FileCredentialExpiry = time.Now().Add(30 * time.Second)
	h.mu.Unlock()
	fresh, err := h.FileAccess(1)
	if err != nil {
		t.Fatal(err)
	}
	freshBytes, err := base64.RawURLEncoding.DecodeString(fresh.Credential)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Credential == old.Credential || !h.AuthenticateFile(1, oldBytes) || !h.AuthenticateFile(1, freshBytes) {
		t.Fatal("rotation broke the overlap window")
	}
	h.mu.Lock()
	h.sessions[1].PreviousFileCredentialExpiry = time.Now().Add(-time.Second)
	h.mu.Unlock()
	if h.AuthenticateFile(1, oldBytes) || !h.AuthenticateFile(1, freshBytes) {
		t.Fatal("expired credential was accepted")
	}
	h.Remove(1)
	if h.AuthenticateFile(1, oldBytes) || h.AuthenticateFile(1, freshBytes) {
		t.Fatal("removed session retained credentials")
	}
}
