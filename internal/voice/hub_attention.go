package voice

import (
	"errors"
	"fmt"
	"time"

	"uniclog.io/sonoryx/internal/domain"
)

const (
	attentionFreeSends = 2
	// A burst with no lock expires after this quiet period, so two old
	// wakes do not turn a later call into a penalty.
	attentionBurstIdle = 2 * time.Minute
)

// attentionLockLadder grows with each rejected wake in a burst.
// The last rung is the cap. When a lock expires, the rung returns to zero.
var attentionLockLadder = []time.Duration{
	10 * time.Second,
	30 * time.Second,
	time.Minute,
	2 * time.Minute,
	5 * time.Minute,
	10 * time.Minute,
}

func attentionLockFor(step int) time.Duration {
	if step < 0 {
		step = 0
	}
	if step >= len(attentionLockLadder) {
		return attentionLockLadder[len(attentionLockLadder)-1]
	}
	return attentionLockLadder[step]
}

func nextAttentionStep(step int) int {
	if step+1 >= len(attentionLockLadder) {
		return len(attentionLockLadder) - 1
	}
	return step + 1
}

func attentionLocked(remain time.Duration) error {
	seconds := int((remain + time.Second - 1) / time.Second)
	if seconds < 1 {
		seconds = 1
	}
	return fmt.Errorf("attention-locked:%d", seconds)
}

func relaxAttention(session *Session, now time.Time) {
	if !session.attentionUntil.IsZero() {
		if now.Before(session.attentionUntil) {
			return
		}
		session.attentionUntil = time.Time{}
		session.attentionStep = 0
		session.attentionUsed = 0
		return
	}
	if session.attentionAt.IsZero() || session.attentionUsed == 0 {
		return
	}
	// A finished pair must not stay spent forever when the client held the
	// pause locally and never opened a server lock. One leftover wake still
	// waits out the longer quiet period.
	idle := attentionBurstIdle
	if session.attentionUsed >= attentionFreeSends {
		idle = attentionLockLadder[0]
	}
	if now.Sub(session.attentionAt) >= idle {
		session.attentionStep = 0
		session.attentionUsed = 0
	}
}

func lockAttention(session *Session, now time.Time) error {
	remain := attentionLockFor(session.attentionStep)
	session.attentionUntil = now.Add(remain)
	session.attentionStep = nextAttentionStep(session.attentionStep)
	session.attentionAt = now
	return attentionLocked(remain)
}

func (h *Hub) SetPresence(id uint64, away bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	session, ok := h.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	if session.ChannelID == 0 {
		return errors.New("presence requires a channel")
	}
	if session.Away == away {
		return nil
	}
	session.Away = away
	h.revision++
	h.emitLocked(domain.StateEvent{Kind: domain.ParticipantPresence, SessionID: id, Away: away})
	return nil
}

// AcceptAttention checks that from and target share a channel.
// The first attentionFreeSends wakes in a burst are allowed. The slot is
// reserved here, before the cue is sent, so a second packet cannot pass the
// same check. ReleaseAttention returns the slot when the cue is not sent.
// The next wake, and every wake while attentionUntil is still ahead, is
// refused. A refusal never delivers a cue and never clears the lock: it
// restarts the current rung and then climbs attentionLockLadder, from 10s
// to 10m. The rung and the burst return to zero only after that time is
// reached. An exhausted burst that never opened a lock clears after the
// first rung; a single wake clears after attentionBurstIdle. The returned
// session is a copy safe to use after the lock is released.
func (h *Hub) AcceptAttention(from, target uint64, now time.Time) (Session, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	sender := h.sessions[from]
	dest := h.sessions[target]
	if sender == nil || dest == nil {
		return Session{}, ErrSessionNotFound
	}
	if from == target || sender.ChannelID == 0 || sender.ChannelID != dest.ChannelID {
		return Session{}, errors.New("participant is not in your channel")
	}
	relaxAttention(sender, now)
	if !sender.attentionUntil.IsZero() && now.Before(sender.attentionUntil) {
		return Session{}, lockAttention(sender, now)
	}
	if sender.attentionUsed >= attentionFreeSends {
		return Session{}, lockAttention(sender, now)
	}
	sender.attentionUsed++
	sender.attentionAt = now
	return *cloneSession(dest), nil
}

// ReleaseAttention returns a reserved free slot when the cue was not sent.
func (h *Hub) ReleaseAttention(from uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	session := h.sessions[from]
	if session == nil || !session.attentionUntil.IsZero() || session.attentionUsed == 0 {
		return
	}
	session.attentionUsed--
}
