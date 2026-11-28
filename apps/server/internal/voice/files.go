package voice

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net"
	"time"

	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func (h *Hub) SetFileEndpoint(port uint16, fingerprint string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.filePort = port
	h.fileFingerprint = fingerprint
}
func (h *Hub) FileAccess(id uint64) (protocol.FileAccess, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s, ok := h.sessions[id]
	if !ok || s.UserID <= 0 {
		return protocol.FileAccess{}, ErrSessionNotFound
	}
	a := protocol.FileAccess{Port: h.filePort, Fingerprint: h.fileFingerprint, Schema: protocol.ChatSchemaVersion}
	if h.filePort == 0 {
		return a, nil
	}
	if time.Until(s.FileCredentialExpiry) < time.Minute {
		var fresh [32]byte
		if _, err := rand.Read(fresh[:]); err != nil {
			return a, err
		}
		s.PreviousFileCredential, s.PreviousFileCredentialExpiry = s.FileCredential, s.FileCredentialExpiry
		s.FileCredential = fresh
		s.FileCredentialExpiry = time.Now().Add(5 * time.Minute)
	}
	a.Credential = base64.RawURLEncoding.EncodeToString(s.FileCredential[:])
	a.Expires = s.FileCredentialExpiry.Unix()
	return a, nil
}
func (h *Hub) AuthenticateFile(id uint64, credential []byte) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	s, ok := h.sessions[id]
	if !ok || len(credential) != 32 {
		return false
	}
	current := subtle.ConstantTimeCompare(s.FileCredential[:], credential)
	previous := subtle.ConstantTimeCompare(s.PreviousFileCredential[:], credential)
	now := time.Now()
	return current == 1 && now.Before(s.FileCredentialExpiry) || previous == 1 && now.Before(s.PreviousFileCredentialExpiry)
}
func HandleFileAccessPacket(conn *udp.ServerPacketConn, hub *Hub, cache *RequestCache, p protocol.VoicePacket, addr *net.UDPAddr) error {
	if err := ValidateSessionAddr(hub, p.SessionID, addr); err != nil {
		return err
	}
	if p.RequestID == 0 || len(p.Payload) != 0 {
		return SendError(conn, addr, p.SessionID, p.RequestID, "invalid file access request")
	}
	access, err := hub.FileAccess(p.SessionID)
	if err != nil {
		return SendError(conn, addr, p.SessionID, p.RequestID, "file access unavailable")
	}
	body, err := json.Marshal(access)
	if err != nil {
		return err
	}
	return conn.WritePacket(p.SessionID, addr, protocol.VoicePacket{Type: protocol.PacketFileAccessAck, SessionID: p.SessionID, RequestID: p.RequestID, Payload: body})
}
