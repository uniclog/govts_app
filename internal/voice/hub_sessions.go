package voice

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

func (h *Hub) Add(s *Session) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.sessions[s.ID] = cloneSession(s)
	h.revision++
}

func (h *Hub) Remove(id uint64) (Session, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	session, ok := h.sessions[id]
	if !ok {
		return Session{}, false
	}
	delete(h.sessions, id)
	h.stopScreenShareByOwnerLocked(id)
	h.revision++
	h.emitLocked(domain.StateEvent{Kind: domain.ParticipantLeft, SessionID: id})
	log.Printf("session removed: id=%d name=%q addr=%v reason=explicit_removal", id, session.Name, session.Addr)
	return *cloneSession(session), true
}

func (h *Hub) Get(id uint64) (Session, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	s, ok := h.sessions[id]
	if !ok {
		return Session{}, false
	}
	return *cloneSession(s), true
}

func (h *Hub) SetAudioState(id uint64, muted, deafened bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.setAudioStateLocked(id, muted, deafened)
}

func (h *Hub) setAudioStateLocked(id uint64, muted, deafened bool) error {
	session, ok := h.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	if session.Muted == muted && session.Deafened == deafened {
		return nil
	}
	if err := h.advanceVoiceIdentityLocked(session, session.Muted != muted, session.Deafened != deafened); err != nil {
		return err
	}
	session.Muted = muted
	session.Deafened = deafened
	h.revision++
	h.emitLocked(domain.StateEvent{Kind: domain.ParticipantAudio, SessionID: id, Muted: muted, Deafened: deafened})
	return nil
}

func participantFromSession(session *Session) domain.Participant {
	return domain.Participant{
		SessionID:   session.ID,
		UserID:      session.UserID,
		DisplayName: session.Name,
		ChannelID:   session.ChannelID,
		Muted:       session.Muted,
		Deafened:    session.Deafened,
		Away:        session.Away,
	}
}

func (h *Hub) Rename(id uint64, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	session, ok := h.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	if session.Name == name {
		return nil
	}
	if err := validateParticipantName(name); err != nil {
		return err
	}
	session.Name = name
	h.revision++
	return nil
}

func (h *Hub) Participants() []domain.Participant {
	h.mu.RLock()
	defer h.mu.RUnlock()

	participants := make([]domain.Participant, 0, len(h.sessions))
	for _, session := range h.sessions {
		participants = append(participants, participantFromSession(session))
	}
	sort.Slice(participants, func(i int, j int) bool {
		return participants[i].SessionID < participants[j].SessionID
	})
	return participants
}

// CreateAuthenticatedSession binds a fresh transport session to a verified
// account. A prior session at the same endpoint is removed, regardless of its
// display name or account, so endpoint reuse cannot inherit another identity.
func (h *Hub) CreateAuthenticatedSession(name string, addr *net.UDPAddr, userID int64, level uint16, permissions uint8, owner bool) (Session, []uint64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := validateParticipantName(name); err != nil {
		return Session{}, nil, err
	}
	if addr == nil || userID <= 0 {
		return Session{}, nil, errors.New("authenticated session requires an account and endpoint")
	}
	// Sessions at this endpoint are replaced, so they do not count against the
	// limits or hold the name.
	remaining, accountSessions := 0, 0
	for _, old := range h.sessions {
		if sameUDPAddr(old.Addr, addr) {
			continue
		}
		remaining++
		if old.UserID == userID {
			accountSessions++
		} else if strings.EqualFold(old.Name, name) {
			return Session{}, nil, ErrDisplayNameTaken
		}
	}
	if remaining >= h.maxSessions {
		return Session{}, nil, ErrServerFull
	}
	if accountSessions >= MaxSessionsPerAccount {
		return Session{}, nil, ErrAccountSessionLimit
	}
	var replaced []uint64
	for id, old := range h.sessions {
		if !sameUDPAddr(old.Addr, addr) {
			continue
		}
		log.Printf("session removed: id=%d name=%q addr=%v reason=endpoint_replaced new_name=%q new_user_id=%d", id, old.Name, old.Addr, name, userID)
		delete(h.sessions, id)
		h.stopScreenShareByOwnerLocked(id)
		h.revision++
		h.emitLocked(domain.StateEvent{Kind: domain.ParticipantLeft, SessionID: id})
		replaced = append(replaced, id)
	}
	id, err := h.availableSessionID()
	if err != nil {
		return Session{}, replaced, err
	}
	session := &Session{ID: id, UserID: userID, JoinLevel: level, Permissions: permissions, Owner: owner, Name: name, Addr: cloneUDPAddr(addr), LastSeen: time.Now()}
	h.sessions[id] = session
	h.revision++
	h.emitLocked(domain.StateEvent{Kind: domain.ParticipantJoined, Participant: domain.Participant{SessionID: id, UserID: userID, DisplayName: name}})
	return *cloneSession(session), replaced, nil
}

func sameUDPAddr(left, right *net.UDPAddr) bool {
	return left != nil && right != nil && left.Port == right.Port && left.IP.Equal(right.IP)
}

func validateParticipantName(name string) error {
	if hasNameControlCharacters(name) {
		return errors.New("invalid participant name: control characters are not allowed")
	}
	if !utf8.ValidString(name) || name == "" || len(name) > domain.MaxParticipantNameBytes || strings.TrimSpace(name) != name {
		return fmt.Errorf("invalid participant name: must be valid UTF-8, trimmed, and 1..%d bytes", domain.MaxParticipantNameBytes)
	}
	return nil
}

func (h *Hub) availableSessionID() (uint64, error) {
	for {
		id, err := h.newSessionID()
		if err != nil {
			return 0, fmt.Errorf("generate session ID: %w", err)
		}
		if id == 0 {
			continue
		}
		if _, exists := h.sessions[id]; !exists {
			return id, nil
		}
	}
}

func randomSessionID() (uint64, error) {
	var data [8]byte
	if _, err := rand.Read(data[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(data[:]), nil
}

func (h *Hub) Touch(sessionID uint64) error {
	return h.touchAt(sessionID, time.Now())
}

func (h *Hub) MediaCredential(sessionID uint64) ([32]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	session, ok := h.sessions[sessionID]
	if !ok {
		return [32]byte{}, ErrSessionNotFound
	}
	if session.MediaCredential == ([32]byte{}) {
		if _, err := rand.Read(session.MediaCredential[:]); err != nil {
			return [32]byte{}, fmt.Errorf("generate media credential: %w", err)
		}
	}
	return session.MediaCredential, nil
}

func (h *Hub) AuthenticateMedia(sessionID uint64, credential [32]byte) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	session, ok := h.sessions[sessionID]
	return ok && credential != ([32]byte{}) && subtle.ConstantTimeCompare(session.MediaCredential[:], credential[:]) == 1
}

func (h *Hub) touchAt(sessionID uint64, now time.Time) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	session, ok := h.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %d not found", sessionID)
	}

	session.LastSeen = now
	return nil
}

func (h *Hub) UpdateAddr(sessionID uint64, addr *net.UDPAddr) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	session, ok := h.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %d not found", sessionID)
	}

	session.Addr = cloneUDPAddr(addr)
	return nil
}

func (h *Hub) RemoveInactive(now time.Time, timeout time.Duration) []Session {
	h.mu.Lock()
	defer h.mu.Unlock()

	var removed []Session
	for id, session := range h.sessions {
		if now.Sub(session.LastSeen) < timeout {
			continue
		}
		delete(h.sessions, id)
		h.stopScreenShareByOwnerLocked(id)
		h.revision++
		h.emitLocked(domain.StateEvent{Kind: domain.ParticipantLeft, SessionID: id})
		removed = append(removed, *cloneSession(session))
	}
	sortSessionsByID(removed)
	return removed
}

func sortSessionsByID(sessions []Session) {
	sort.Slice(sessions, func(i int, j int) bool {
		return sessions[i].ID < sessions[j].ID
	})
}

func cloneSession(session *Session) *Session {
	if session == nil {
		return nil
	}

	clone := *session
	clone.Addr = cloneUDPAddr(session.Addr)
	// Transport statistics are private to the hub and not part of session snapshots.
	clone.voiceArrivals = nil
	clone.voiceSeen = nil
	clone.lastVoice = protocol.VoiceBundleFrame{}
	clone.voiceControl = nil
	clone.voiceFrames = nil
	clone.voiceFlow = nil
	return &clone
}

func cloneUDPAddr(addr *net.UDPAddr) *net.UDPAddr {
	if addr == nil {
		return nil
	}

	clone := *addr
	clone.IP = append(net.IP(nil), addr.IP...)
	return &clone
}
