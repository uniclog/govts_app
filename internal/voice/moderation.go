package voice

import (
	"errors"
	"fmt"
	"log"
	"sort"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/persist"
)

var ErrPermissionDenied = errors.New("permission denied")

func (h *Hub) checkModerationLocked(actorID, targetID uint64, permission uint8) (*Session, error) {
	actor := h.sessions[actorID]
	target := h.sessions[targetID]
	if actor == nil || target == nil {
		return nil, ErrSessionNotFound
	}
	if actor.UserID <= 0 || actor.Permissions&permission == 0 {
		return nil, ErrPermissionDenied
	}
	if actorID == targetID && permission != persist.PermissionDrag {
		return nil, ErrPermissionDenied
	}
	sameAccount := actor.UserID == target.UserID
	if sameAccount && permission != persist.PermissionDrag {
		return nil, ErrPermissionDenied
	}
	if target.Owner && !sameAccount {
		return nil, ErrPermissionDenied
	}
	return target, nil
}

func (h *Hub) CheckModeration(actorID, targetID uint64, permission uint8) (Session, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	target, err := h.checkModerationLocked(actorID, targetID, permission)
	if err != nil {
		return Session{}, err
	}
	return *cloneSession(target), nil
}

func (h *Hub) Kick(actorID, targetID uint64) (Session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	target, err := h.checkModerationLocked(actorID, targetID, persist.PermissionKick)
	if err != nil {
		return Session{}, err
	}
	removed := *cloneSession(target)
	delete(h.sessions, targetID)
	h.stopScreenShareByOwnerLocked(targetID)
	h.revision++
	h.emitLocked(domain.StateEvent{Kind: domain.ParticipantLeft, SessionID: targetID})
	log.Printf("session removed: id=%d user_id=%d name=%q addr=%v reason=kick actor_session_id=%d", targetID, target.UserID, target.Name, target.Addr, actorID)
	return removed, nil
}

func (h *Hub) Drag(actorID, targetID uint64, channelID domain.ChannelID) (domain.StateRevision, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, err := h.checkModerationLocked(actorID, targetID, persist.PermissionDrag); err != nil {
		return 0, err
	}
	if err := h.joinChannelLocked(targetID, channelID); err != nil {
		return 0, err
	}
	return h.revision, nil
}

// CheckDrag validates the requested move without changing Hub state, so
// durable audit storage can be checked before the move is applied.
func (h *Hub) CheckDrag(actorID, targetID uint64, channelID domain.ChannelID) (Session, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	target, err := h.checkModerationLocked(actorID, targetID, persist.PermissionDrag)
	if err != nil {
		return Session{}, err
	}
	channel := h.channels[channelID]
	if channel == nil {
		return Session{}, fmt.Errorf("%w: %d", ErrChannelNotFound, channelID)
	}
	if target.ChannelID != channelID {
		if target.JoinLevel < channel.MinJoinLevel {
			return Session{}, ErrChannelForbidden
		}
		if channel.MaxUsers > 0 && h.channelMemberCountLocked(channelID) >= channel.MaxUsers {
			return Session{}, fmt.Errorf("%w: %q", ErrChannelFull, channel.Name)
		}
	}
	return *cloneSession(target), nil
}

func (h *Hub) RemoveUserSessions(userID int64) []uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	var removed []uint64
	for id, session := range h.sessions {
		if session.UserID != userID {
			continue
		}
		log.Printf("session removed: id=%d user_id=%d name=%q addr=%v reason=account_sessions_removed", id, userID, session.Name, session.Addr)
		delete(h.sessions, id)
		h.stopScreenShareByOwnerLocked(id)
		h.revision++
		h.emitLocked(domain.StateEvent{Kind: domain.ParticipantLeft, SessionID: id})
		removed = append(removed, id)
	}
	sort.Slice(removed, func(i, j int) bool { return removed[i] < removed[j] })
	return removed
}

func (h *Hub) ApplyPermissions(userID int64, permissions uint8) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, session := range h.sessions {
		if session.UserID == userID {
			session.Permissions = permissions
		}
	}
}

func (h *Hub) ApplyOwner(userID int64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, session := range h.sessions {
		if session.UserID == userID {
			session.Owner = true
		}
	}
}
