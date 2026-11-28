package voice

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"path/filepath"
	"testing"
	"time"

	"uniclog.io/sonoryx/apps/server/internal/persist"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

// CreateSession adds a session without an account. Servers only create
// sessions through CreateAuthenticatedSession; tests use this to seed a hub.
func (h *Hub) CreateSession(name string, addr *net.UDPAddr) (Session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := validateParticipantName(name); err != nil {
		return Session{}, err
	}
	id, err := h.availableSessionID()
	if err != nil {
		return Session{}, err
	}
	session := &Session{ID: id, Name: name, Addr: cloneUDPAddr(addr), LastSeen: time.Now()}
	h.sessions[id] = session
	h.revision++
	h.emitLocked(domain.StateEvent{Kind: domain.ParticipantJoined, Participant: domain.Participant{SessionID: id, DisplayName: name}})
	return *cloneSession(session), nil
}

// newTestAuthenticator returns an authenticator backed by a temporary store
// and a fresh server key.
func newTestAuthenticator(t *testing.T, codec *protocol.SecureDatagramCodec) *Authenticator {
	t.Helper()
	store, err := persist.Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_, serverKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authenticator, err := NewAuthenticator(serverKey, store, codec)
	if err != nil {
		t.Fatal(err)
	}
	return authenticator
}
