package voice

import (
	"errors"
	"log"
	"net"
	"sort"
	"time"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

// Watcher is an authenticated session that only receives notices. It is kept
// apart from Hub.sessions, so it never appears in snapshots, events, the online
// count, voice routing or any request handler: a client subscribed to a server
// it is not connected to learns about arrivals and messages without being seen.
type Watcher struct {
	ID        uint64
	UserID    int64
	JoinLevel uint16
	Addr      *net.UDPAddr
	LastSeen  time.Time
}

var ErrWatcherNeedsAccount = errors.New("watch requires an existing account")

// CreateWatcher registers a watcher for a verified account. A session or
// watcher at the same endpoint is replaced, as for participants.
func (h *Hub) CreateWatcher(addr *net.UDPAddr, userID int64, level uint16) (Watcher, []uint64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if addr == nil || userID <= 0 {
		return Watcher{}, nil, errors.New("watcher requires an account and endpoint")
	}
	remaining, account := 0, 0
	for _, old := range h.watchers {
		if sameUDPAddr(old.Addr, addr) {
			continue
		}
		remaining++
		if old.UserID == userID {
			account++
		}
	}
	if remaining >= h.maxSessions {
		return Watcher{}, nil, ErrServerFull
	}
	if account >= MaxSessionsPerAccount {
		return Watcher{}, nil, ErrAccountSessionLimit
	}
	replaced := h.removeEndpointLocked(addr, "watcher_endpoint_replaced")
	id, err := h.availableSessionID()
	if err != nil {
		return Watcher{}, replaced, err
	}
	watcher := &Watcher{ID: id, UserID: userID, JoinLevel: level, Addr: cloneUDPAddr(addr), LastSeen: time.Now()}
	h.watchers[id] = watcher
	return cloneWatcher(watcher), replaced, nil
}

// removeEndpointLocked drops every session and watcher bound to addr.
func (h *Hub) removeEndpointLocked(addr *net.UDPAddr, reason string) []uint64 {
	var replaced []uint64
	for id, old := range h.sessions {
		if !sameUDPAddr(old.Addr, addr) {
			continue
		}
		log.Printf("session removed: id=%d name=%q addr=%v reason=%s", id, old.Name, old.Addr, reason)
		delete(h.sessions, id)
		h.stopScreenShareByOwnerLocked(id)
		h.revision++
		h.emitLocked(domain.StateEvent{Kind: domain.ParticipantLeft, SessionID: id})
		replaced = append(replaced, id)
	}
	for id, old := range h.watchers {
		if sameUDPAddr(old.Addr, addr) {
			delete(h.watchers, id)
			replaced = append(replaced, id)
		}
	}
	return replaced
}

// Watcher returns a copy of a registered watcher.
func (h *Hub) Watcher(id uint64) (Watcher, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	watcher, ok := h.watchers[id]
	if !ok {
		return Watcher{}, false
	}
	return cloneWatcher(watcher), true
}

// Known reports whether id is a participant session or a watcher.
func (h *Hub) Known(id uint64) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, session := h.sessions[id]
	_, watcher := h.watchers[id]
	return session || watcher
}

func (h *Hub) touchWatcher(id uint64, addr *net.UDPAddr, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	watcher, ok := h.watchers[id]
	if !ok || !sameUDPAddr(watcher.Addr, addr) {
		return false
	}
	watcher.LastSeen = now
	return true
}

func (h *Hub) RemoveWatcher(id uint64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.watchers[id]; !ok {
		return false
	}
	delete(h.watchers, id)
	return true
}

// removeInactiveWatchersLocked expires watchers like sessions. Callers drop
// the returned IDs from the secure codec and the request cache.
func (h *Hub) removeInactiveWatchersLocked(now time.Time, timeout time.Duration) []Session {
	var removed []Session
	for id, watcher := range h.watchers {
		if now.Sub(watcher.LastSeen) < timeout {
			continue
		}
		delete(h.watchers, id)
		removed = append(removed, Session{ID: id, UserID: watcher.UserID, Name: "(watcher)", Addr: cloneUDPAddr(watcher.Addr), LastSeen: watcher.LastSeen})
	}
	return removed
}

func (h *Hub) removeUserWatchersLocked(userID int64) []uint64 {
	var removed []uint64
	for id, watcher := range h.watchers {
		if watcher.UserID == userID {
			delete(h.watchers, id)
			removed = append(removed, id)
		}
	}
	return removed
}

func (h *Hub) applyWatcherJoinLevelLocked(userID int64, level uint16) {
	for _, watcher := range h.watchers {
		if watcher.UserID == userID {
			watcher.JoinLevel = level
		}
	}
}

func cloneWatcher(w *Watcher) Watcher {
	copy := *w
	copy.Addr = cloneUDPAddr(w.Addr)
	return copy
}

// HandleWatcherPacket serves the only requests a watcher may send: heartbeat
// and disconnect. It reports false for a packet that is not a watcher's.
func HandleWatcherPacket(conn *udp.ServerPacketConn, hub *Hub, cache *RequestCache, packet protocol.VoicePacket, addr *net.UDPAddr) (bool, error) {
	if _, ok := hub.Watcher(packet.SessionID); !ok {
		return false, nil
	}
	switch packet.Type {
	case protocol.PacketHeartbeat:
		if packet.RequestID == 0 {
			return true, nil
		}
		if !hub.touchWatcher(packet.SessionID, addr, time.Now()) {
			return true, conn.WritePacket(packet.SessionID, addr, protocol.VoicePacket{Type: protocol.PacketSessionInvalid, SessionID: packet.SessionID, RequestID: packet.RequestID})
		}
		return true, conn.WritePacket(packet.SessionID, addr, protocol.VoicePacket{Type: protocol.PacketHeartbeatAck, SessionID: packet.SessionID, RequestID: packet.RequestID})
	case protocol.PacketDisconnect:
		if watcher, ok := hub.Watcher(packet.SessionID); ok && sameUDPAddr(watcher.Addr, addr) && hub.RemoveWatcher(packet.SessionID) {
			cache.RemoveSession(packet.SessionID)
		}
		return true, nil
	default:
		// Anything else would treat the watcher as a participant.
		return true, SendError(conn, addr, packet.SessionID, packet.RequestID, "watcher sessions accept heartbeats only")
	}
}

// noticeTarget is one recipient of a notice: a participant session or a watcher.
type noticeTarget struct {
	ID   uint64
	Addr *net.UDPAddr
}

// arrivalRecipients are the notice-enabled sessions and the watchers of other accounts.
func (h *Hub) arrivalRecipients(userID int64) []noticeTarget {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []noticeTarget
	for _, s := range h.sessions {
		if s.notices && s.UserID != userID {
			out = append(out, noticeTarget{s.ID, cloneUDPAddr(s.Addr)})
		}
	}
	for _, w := range h.watchers {
		if w.UserID != userID {
			out = append(out, noticeTarget{w.ID, cloneUDPAddr(w.Addr)})
		}
	}
	sortNoticeTargets(out)
	return out
}

// messageRecipients are the notice-enabled sessions and watchers that may read
// a new message. For a channel: sessions in it, subscribed sessions and watchers
// whose level admits the channel. For a dialog: the recipient's sessions.
// The sender's own sessions never get a notice.
func (h *Hub) messageRecipients(sender int64, target domain.ChatTarget) ([]noticeTarget, string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []noticeTarget
	if target.Kind == domain.ChatDirect {
		for _, s := range h.sessions {
			if s.notices && s.UserID == target.ID {
				out = append(out, noticeTarget{s.ID, cloneUDPAddr(s.Addr)})
			}
		}
		for _, w := range h.watchers {
			if w.UserID == target.ID {
				out = append(out, noticeTarget{w.ID, cloneUDPAddr(w.Addr)})
			}
		}
		sortNoticeTargets(out)
		return out, ""
	}
	channel := h.channels[domain.ChannelID(target.ID)]
	if channel == nil {
		return nil, ""
	}
	for _, s := range h.sessions {
		if !s.notices || s.UserID == sender {
			continue
		}
		if s.ChannelID == channel.ID || (s.subscribed && s.JoinLevel >= channel.MinJoinLevel) {
			out = append(out, noticeTarget{s.ID, cloneUDPAddr(s.Addr)})
		}
	}
	for _, w := range h.watchers {
		if w.UserID != sender && w.JoinLevel >= channel.MinJoinLevel {
			out = append(out, noticeTarget{w.ID, cloneUDPAddr(w.Addr)})
		}
	}
	sortNoticeTargets(out)
	return out, channel.Name
}

func sortNoticeTargets(targets []noticeTarget) {
	sort.Slice(targets, func(i, j int) bool { return targets[i].ID < targets[j].ID })
}
