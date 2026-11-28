package voice

import (
	"bytes"
	"errors"
	"math"
	"sort"
	"sync"
	"time"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

var ErrVoiceEpochExhausted = errors.New("voice identity exhausted; reconnect required")
var ErrStaleVoiceControl = errors.New("voice control revision is stale")

type VoiceControlResult struct {
	Session        Session
	VoiceRevision  uint64
	DomainRevision domain.StateRevision
}

func (h *Hub) voiceControlResultLocked(id uint64) VoiceControlResult {
	result := VoiceControlResult{DomainRevision: h.revision}
	if session := h.sessions[id]; session != nil {
		result.Session = *cloneSession(session)
		if session.voiceControl != nil {
			result.VoiceRevision = session.voiceControl.revision
		}
	}
	return result
}

// The revision check and mutation share one Hub lock. Runtime handlers must
// cache the result by RequestID before replying, including stale responses.
func (h *Hub) JoinChannelAtVoiceRevision(id uint64, channelID domain.ChannelID, expected uint64) (VoiceControlResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	session := h.sessions[id]
	if session == nil {
		return VoiceControlResult{}, ErrSessionNotFound
	}
	ensureVoiceIdentity(session)
	if expected != session.OwnControlRevision {
		return h.voiceControlResultLocked(id), ErrStaleVoiceControl
	}
	err := h.joinChannelLocked(id, channelID)
	return h.voiceControlResultLocked(id), err
}

func (h *Hub) SetAudioStateAtVoiceRevision(id uint64, muted, deafened bool, expected uint64) (VoiceControlResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	session := h.sessions[id]
	if session == nil {
		return VoiceControlResult{}, ErrSessionNotFound
	}
	ensureVoiceIdentity(session)
	if expected != session.OwnControlRevision {
		return h.voiceControlResultLocked(id), ErrStaleVoiceControl
	}
	err := h.setAudioStateLocked(id, muted, deafened)
	return h.voiceControlResultLocked(id), err
}

type voiceControlSession struct {
	writeMu          sync.Mutex
	revision         uint64
	ackRevision      uint64
	ackReceiverEpoch uint32
	ackSources       map[uint64]uint32
	pending          *voiceStatePending
	dirty            bool
	lastRequest      time.Time
	lastDatagram     uint32
	barrier          uint32
	profileRevision  uint64
	stateBudget      voiceStateByteBudget
}

type voiceStatePending struct {
	revision      uint64
	receiverEpoch uint32
	sources       map[uint64]uint32
	payloads      [][]byte
	next          time.Time
	attempt       int
}

type VoiceStateDelivery struct {
	Session Session
	Packets []protocol.VoicePacket
}

func ensureVoiceIdentity(session *Session) {
	if session.SourceEpoch == 0 {
		session.SourceEpoch = 1
	}
	if session.ReceiverEpoch == 0 {
		session.ReceiverEpoch = 1
	}
	if session.OwnControlRevision == 0 {
		session.OwnControlRevision = 1
	}
}

// Called before changing public channel/audio fields, under h.mu. Exhausted
// identities are retired so a heartbeat forces a fresh authenticated session.
func (h *Hub) advanceVoiceIdentityLocked(session *Session, source, receiver bool) error {
	if (source && session.SourceEpoch == math.MaxUint32) || (receiver && session.ReceiverEpoch == math.MaxUint32) || session.OwnControlRevision == math.MaxUint64 {
		h.retireVoiceSessionLocked(session)
		return ErrVoiceEpochExhausted
	}
	if source {
		session.voiceFrames = nil
		session.SourceEpoch++
		session.lastVoice = protocol.VoiceBundleFrame{}
		session.voiceSequence, session.voiceSequenceAt = 0, time.Time{}
	}
	if receiver {
		session.ReceiverEpoch++
		if session.voiceControl != nil {
			session.voiceControl.barrier = session.voiceControl.lastDatagram
		}
	}
	session.OwnControlRevision++
	ensureVoiceIdentity(session)
	return nil
}

func (h *Hub) retireVoiceSessionLocked(session *Session) {
	delete(h.sessions, session.ID)
	h.stopScreenShareByOwnerLocked(session.ID)
	h.revision++
	h.emitLocked(domain.StateEvent{Kind: domain.ParticipantLeft, SessionID: session.ID})
}

func (h *Hub) invalidateVoiceStatesLocked(event domain.StateEvent) {
	for _, session := range h.sessions {
		control := session.voiceControl
		if control == nil {
			continue
		}
		_, known := control.ackSources[event.SessionID]
		if control.pending != nil {
			_, pending := control.pending.sources[event.SessionID]
			known = known || pending
		}
		affected := false
		switch event.Kind {
		case domain.ParticipantMoved:
			affected = session.ID == event.SessionID || session.ChannelID == event.ChannelID || known
		case domain.ParticipantLeft:
			affected = known
		case domain.ParticipantJoined:
			affected = event.Participant.ChannelID != 0 && session.ChannelID == event.Participant.ChannelID
		case domain.ParticipantAudio:
			source := h.sessions[event.SessionID]
			if source != nil && source.ChannelID == session.ChannelID {
				affected = session.ID == source.ID
				if !affected {
					// Peers only need a source epoch change. Their deafen flag
					// does not change any entry in this recipient's snapshot.
					ackEpoch, ackKnown := control.ackSources[source.ID]
					pendingKnown := control.pending != nil && control.pending.sources[source.ID] == source.SourceEpoch
					affected = !pendingKnown && (!ackKnown || ackEpoch != source.SourceEpoch)
				}
			}
		case domain.ChannelUpdated:
			// Profile updates assign this exact domain revision before emit.
			// Topic/name/reordering changes do not change voice state.
			affected = session.ChannelID == event.Channel.ID && control.profileRevision == uint64(event.Revision)
		}
		if affected && (!control.dirty || control.revision == control.ackRevision) {
			// Never wrap revision into a previously acknowledged identity.
			if control.revision == math.MaxUint64 {
				// The identity is unusable; retire after this traversal to avoid
				// recursively publishing events while iterating the same state.
				control.dirty = true
				control.pending = nil
				continue
			}
			control.revision++
			control.dirty = true
			control.pending = nil
		}
	}
}

// RequestVoiceState activates delivery after the client joins a channel. The
// request gate is separate from transport/heartbeat lifetime and timers.
func (h *Hub) RequestVoiceState(id uint64, now time.Time) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	session := h.sessions[id]
	if session == nil {
		return ErrSessionNotFound
	}
	if session.ChannelID == 0 {
		return ErrSessionNotInChannel
	}
	ensureVoiceIdentity(session)
	if session.voiceControl == nil {
		session.voiceControl = &voiceControlSession{revision: 1, dirty: true, profileRevision: uint64(h.revision)}
	}
	h.voiceEndpointLocked(session, now)
	control := session.voiceControl
	if !control.lastRequest.IsZero() && now.Sub(control.lastRequest) < time.Second {
		return nil
	}
	control.lastRequest = now
	if control.pending != nil {
		control.pending.next = now
	} else {
		control.dirty = true
	}
	return nil
}

func (h *Hub) voiceStatePendingLocked(session *Session) (*voiceStatePending, error) {
	control := session.voiceControl
	flags := protocol.VoiceStateFull
	if session.Muted {
		flags |= protocol.VoiceStateOwnMuted
	}
	if session.Deafened {
		flags |= protocol.VoiceStateOwnDeafened
	}
	header := protocol.VoiceStatePage{
		Flags: flags, Revision: control.revision, OwnControlRevision: session.OwnControlRevision,
		OwnSourceEpoch: session.SourceEpoch, OwnReceiverEpoch: session.ReceiverEpoch,
		OwnChannelID: uint64(session.ChannelID), AudioProfileRevision: control.profileRevision,
		DownstreamBarrier: control.barrier,
	}
	if h.voiceBundles {
		header.EnabledDirections |= protocol.VoiceDirectionDown
	}
	if h.voiceUplink {
		header.EnabledDirections |= protocol.VoiceDirectionUp
	}
	sources := make([]protocol.VoiceSourceState, 0)
	for _, source := range h.sessions {
		if source.ChannelID != session.ChannelID {
			continue
		}
		ensureVoiceIdentity(source)
		entry := protocol.VoiceSourceState{SenderID: source.ID, SourceEpoch: source.SourceEpoch, ChannelID: uint64(source.ChannelID)}
		if source.Muted {
			entry.Flags |= protocol.VoiceSourceMuted
		}
		sources = append(sources, entry)
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].SenderID < sources[j].SenderID })
	pages, err := protocol.PaginateVoiceState(header, sources)
	if err != nil {
		return nil, err
	}
	pending := &voiceStatePending{revision: control.revision, receiverEpoch: session.ReceiverEpoch, sources: make(map[uint64]uint32, len(sources))}
	for _, source := range sources {
		pending.sources[source.SenderID] = source.SourceEpoch
	}
	for _, page := range pages {
		payload, err := protocol.EncodeVoiceState(page)
		if err != nil {
			return nil, err
		}
		pending.payloads = append(pending.payloads, payload)
	}
	return pending, nil
}

// DueVoiceStates copies ready packets under h.mu. Network writes happen after
// the lock is released. One immutable pending snapshot replaces older changes;
// no audio or unbounded chain of revisions is queued here.
func (h *Hub) DueVoiceStates(now time.Time) []VoiceStateDelivery {
	h.mu.Lock()
	defer h.mu.Unlock()
	var deliveries []VoiceStateDelivery
	recipientRate, globalRate := h.voiceStateRecipientRate, h.voiceStateGlobalRate
	if recipientRate <= 0 {
		recipientRate = voiceStateRecipientBytesPerSecond
	}
	if globalRate <= 0 {
		globalRate = voiceStateGlobalBytesPerSecond
	}
	// Traverse recipients fairly and count channel members once. Reserving a
	// whole immutable snapshot avoids partially budget-starved assemblies and
	// bounds snapshot construction as well as outgoing bytes during churn.
	ids := make([]uint64, 0, len(h.sessions))
	members := make(map[domain.ChannelID]int)
	for _, session := range h.sessions {
		members[session.ChannelID]++
		if session.voiceControl != nil {
			ids = append(ids, session.ID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	start := sort.Search(len(ids), func(i int) bool { return ids[i] > h.voiceStateCursor })
	for i := 0; i < len(ids); i++ {
		session := h.sessions[ids[(start+i)%len(ids)]]
		if session == nil {
			continue
		}
		control := session.voiceControl
		if control == nil {
			continue
		}
		if control.revision == math.MaxUint64 {
			h.retireVoiceSessionLocked(session)
			continue
		}
		pending := control.pending
		if !control.dirty && (pending == nil || now.Before(pending.next)) {
			continue
		}
		cost := voiceStateSnapshotWireSize(members[session.ChannelID])
		if !control.dirty {
			cost = 0
			for _, payload := range pending.payloads {
				cost += len(payload) + voiceStatePacketOverhead
			}
		}
		if !control.stateBudget.canSpend(now, cost, recipientRate, voiceStateRecipientBurstBytes) {
			h.voiceStateDeferrals++
			continue
		}
		if !h.voiceStateBudget.canSpend(now, cost, globalRate, voiceStateGlobalBurstBytes) {
			h.voiceStateDeferrals++
			// Keep this recipient at the head until the next refill rather
			// than letting smaller snapshots consume its reserved turn.
			break
		}
		if control.dirty {
			pending, err := h.voiceStatePendingLocked(session)
			if err != nil {
				continue
			}
			control.pending, control.dirty = pending, false
		}
		pending = control.pending
		control.stateBudget.spend(cost)
		h.voiceStateBudget.spend(cost)
		h.voiceStateCursor = session.ID
		delivery := VoiceStateDelivery{Session: *cloneSession(session)}
		for _, payload := range pending.payloads {
			delivery.Packets = append(delivery.Packets, protocol.VoicePacket{Type: protocol.PacketVoiceState, SessionID: session.ID, Payload: bytes.Clone(payload)})
		}
		delays := [...]time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second}
		pending.next = now.Add(delays[min(pending.attempt, len(delays)-1)])
		pending.attempt = min(pending.attempt+1, len(delays)-1)
		deliveries = append(deliveries, delivery)
	}
	return deliveries
}

// Ack is idempotent and exact: an old or invented revision cannot authorize a
// new speaker generation or the recipient's new receiver epoch.
func (h *Hub) AckVoiceState(id, revision uint64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	session := h.sessions[id]
	if session == nil || session.voiceControl == nil {
		return false
	}
	control := session.voiceControl
	if revision != 0 && revision == control.ackRevision {
		return true
	}
	pending := control.pending
	if pending == nil || pending.revision != revision || revision == 0 {
		return false
	}
	control.ackRevision, control.ackReceiverEpoch = revision, pending.receiverEpoch
	control.ackSources = pending.sources
	control.pending = nil
	return true
}

func (h *Hub) VoiceRelayReady(recipientID, senderID uint64, sourceEpoch, receiverEpoch uint32) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	recipient, sender := h.sessions[recipientID], h.sessions[senderID]
	if sourceEpoch == 0 || receiverEpoch == 0 || recipient == nil || sender == nil || recipientID == senderID || recipient.Deafened || sender.Muted ||
		recipient.ChannelID == 0 || recipient.ChannelID != sender.ChannelID || sender.SourceEpoch != sourceEpoch || recipient.ReceiverEpoch != receiverEpoch {
		return false
	}
	control := recipient.voiceControl
	return control != nil && control.ackReceiverEpoch == receiverEpoch && control.ackSources[senderID] == sourceEpoch
}

// AllocateVoiceDatagram does not write the socket. The runtime must serialize
// allocation + write per recipient and perform all media policy checks first.
func (h *Hub) AllocateVoiceDatagram(id uint64) (uint32, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	session := h.sessions[id]
	if session == nil {
		return 0, ErrSessionNotFound
	}
	if session.voiceControl == nil {
		return 0, ErrSessionNotInChannel
	}
	session.voiceControl.lastDatagram++
	return session.voiceControl.lastDatagram, nil
}

func (h *Hub) SetVoiceUplink(enabled bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.voiceUplink == enabled {
		return
	}
	h.voiceUplink = enabled
	h.invalidateVoiceConfigLocked()
}

func (h *Hub) invalidateVoiceConfigLocked() {
	for _, session := range h.sessions {
		if control := session.voiceControl; control != nil && control.revision != math.MaxUint64 && (!control.dirty || control.revision == control.ackRevision) {
			control.revision++
			control.dirty, control.pending = true, nil
		}
	}
}
