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
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"uniclog.io/sonoryx/internal/appversion"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/voiceflow"
)

var (
	ErrSessionNotFound       = errors.New("session not found")
	ErrSessionNotInChannel   = errors.New("session has not joined a channel")
	ErrChannelNotFound       = errors.New("channel not found")
	ErrChannelFull           = errors.New("channel is full")
	ErrChannelForbidden      = errors.New("channel join level is too low")
	ErrChannelNameTaken      = errors.New("channel name is already in use")
	ErrInvalidChannel        = errors.New("invalid channel")
	ErrInvalidServerInfo     = errors.New("invalid server info")
	ErrScreenStreamExists    = errors.New("session already has an active screen stream")
	ErrScreenStreamNotFound  = errors.New("screen stream not found")
	ErrScreenStreamForbidden = errors.New("screen stream is not owned by session")
	ErrServerFull            = errors.New("server is full")
	ErrAccountSessionLimit   = errors.New("too many sessions for this account")
	ErrDisplayNameTaken      = errors.New("display name is already in use")
)

const (
	DefaultChannelID   domain.ChannelID = 1
	DefaultChannelName                  = "default"
	DefaultServerName                   = "ReCon Server"
	// DefaultMaxSessions bounds online sessions unless SetMaxSessions changes it.
	DefaultMaxSessions = 1000
	// MaxSessionsPerAccount bounds the sessions one account holds at once.
	MaxSessionsPerAccount = 4
)

type sessionIDGenerator func() (uint64, error)

type Hub struct {
	filePort                                      uint16
	fileFingerprint                               string
	voiceStateRecipientRate, voiceStateGlobalRate int
	voiceStateDeferrals                           uint64
	voiceReserve                                  int
	voicePool                                     *voiceflow.Pool
	mu                                            sync.RWMutex
	sessions                                      map[uint64]*Session
	screenStreams                                 map[domain.StreamID]domain.ScreenStream
	streamByOwner                                 map[uint64]domain.StreamID
	channels                                      map[domain.ChannelID]*domain.Channel
	newSessionID                                  sessionIDGenerator
	nextChannelID                                 domain.ChannelID
	revision                                      domain.StateRevision
	serverInfo                                    domain.ServerInfo
	serverVersion                                 appversion.Number
	voiceBundles                                  bool
	voiceUplink                                   bool
	voiceStateBudget                              voiceStateByteBudget
	voiceStateCursor                              uint64
	publicStatus                                  bool
	maxSessions                                   int
	outbox                                        []domain.StateEvent
	eventReady                                    chan struct{}
}

// OperationalSnapshot is a point-in-time copy for local server diagnostics.
// Unlike the future client state snapshot, it intentionally contains runtime
// session data such as UDP endpoints and LastSeen.
type OperationalSnapshot struct {
	VoiceControlBudgetDeferrals uint64
	Revision                    domain.StateRevision
	ServerInfo                  domain.ServerInfo
	Channels                    []domain.Channel
	Sessions                    []Session
}

func NewHub() *Hub {
	hub, err := newHubWithServerInfo(randomSessionID, domain.ServerInfo{Name: DefaultServerName}, true)
	if err != nil {
		panic(err)
	}
	return hub
}

func newHub(newSessionID sessionIDGenerator) *Hub {
	hub, err := newHubWithServerInfo(newSessionID, domain.ServerInfo{Name: DefaultServerName}, true)
	if err != nil {
		panic(err)
	}
	return hub
}

func NewHubWithServerInfo(info domain.ServerInfo) (*Hub, error) {
	return newHubWithServerInfo(randomSessionID, info, true)
}

// NewEmptyHubWithServerInfo creates a hub whose complete channel tree will be
// supplied by an external bootstrap source.
func NewEmptyHubWithServerInfo(info domain.ServerInfo) (*Hub, error) {
	return newHubWithServerInfo(randomSessionID, info, false)
}

func newHubWithServerInfo(newSessionID sessionIDGenerator, info domain.ServerInfo, includeDefault bool) (*Hub, error) {
	if newSessionID == nil {
		panic("session ID generator is required")
	}
	if !utf8.ValidString(info.Name) || info.Name == "" || len(info.Name) > domain.MaxServerNameBytes || strings.TrimSpace(info.Name) != info.Name {
		return nil, fmt.Errorf("%w: server name must be valid UTF-8, trimmed, and 1..%d bytes", ErrInvalidServerInfo, domain.MaxServerNameBytes)
	}
	if hasNameControlCharacters(info.Name) {
		return nil, fmt.Errorf("%w: server name contains control characters", ErrInvalidServerInfo)
	}

	hub := &Hub{
		sessions:      make(map[uint64]*Session),
		screenStreams: make(map[domain.StreamID]domain.ScreenStream),
		streamByOwner: make(map[uint64]domain.StreamID),
		channels:      make(map[domain.ChannelID]*domain.Channel),
		newSessionID:  newSessionID,
		nextChannelID: DefaultChannelID,
		serverInfo:    info,
		serverVersion: 1 << 16, // 0.1.0 for in-process test hubs
		maxSessions:   DefaultMaxSessions,
		eventReady:    make(chan struct{}, 1),
	}
	if includeDefault {
		defaultChannel := domain.Channel{
			ID:    DefaultChannelID,
			Name:  DefaultChannelName,
			Type:  domain.ChannelTypePermanent,
			Audio: domain.DefaultAudioProfile(),
		}
		hub.channels[DefaultChannelID] = &defaultChannel
		hub.serverInfo.DefaultChannelID = DefaultChannelID
		hub.nextChannelID = DefaultChannelID + 1
		hub.revision = 1
	}
	return hub, nil
}

// SetDefaultChannel selects the unrestricted channel used for every new connection.
func (h *Hub) SetDefaultChannel(id domain.ChannelID) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	channel := h.channels[id]
	if channel == nil || channel.MinJoinLevel != 0 || channel.MaxUsers != 0 {
		return fmt.Errorf("%w: default channel must exist, have join level 0 and no user limit", ErrInvalidChannel)
	}
	h.serverInfo.DefaultChannelID = id
	return nil
}

func (h *Hub) SetServerVersion(version appversion.Number) {
	h.mu.Lock()
	h.serverVersion = version
	h.mu.Unlock()
}

func (h *Hub) ServerVersion() appversion.Number {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.serverVersion
}

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

// SetMaxSessions bounds online sessions; values below 1 keep the current limit.
func (h *Hub) SetMaxSessions(limit int) {
	if limit < 1 {
		return
	}
	h.mu.Lock()
	h.maxSessions = limit
	h.mu.Unlock()
}

// SetVoiceBundles enables server → client voice redundancy for sessions that
// support PacketVoiceBundle.
func (h *Hub) SetVoiceBundles(enabled bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.voiceBundles == enabled {
		return
	}
	h.voiceBundles = enabled
	for _, s := range h.sessions {
		if s.voiceFlow != nil {
			s.voiceFlow.SetEnabled(enabled)
		}
	}
	h.invalidateVoiceConfigLocked()
	if !enabled {
		for _, session := range h.sessions {
			session.lastVoice = protocol.VoiceBundleFrame{}
		}
	}
}

// SetSessionVoiceBundles records whether the session's client accepts
// PacketVoiceBundle.
func (h *Hub) SetSessionVoiceBundles(id uint64, supported bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.sessions[id]; s != nil {
		s.VoiceBundles = supported
	}
}

// SwapLastVoiceFrame stores frame as the sender's latest voice frame and
// returns the previous one when it directly precedes frame. Redundancy is
// disabled, duplicate, reordered and discontinuous frames return false.
func (h *Hub) SwapLastVoiceFrame(id uint64, frame protocol.VoiceBundleFrame) (protocol.VoiceBundleFrame, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[id]
	if !h.voiceBundles || s == nil || frame.Sequence == 0 {
		return protocol.VoiceBundleFrame{}, false
	}
	previous := s.lastVoice
	if previous.Sequence != 0 && int32(frame.Sequence-previous.Sequence) <= 0 {
		return protocol.VoiceBundleFrame{}, false
	}
	s.lastVoice = protocol.VoiceBundleFrame{
		Sequence: frame.Sequence,
		Payload:  append([]byte(nil), frame.Payload...),
	}
	if previous.Sequence == 0 || frame.Sequence-previous.Sequence != 1 {
		return protocol.VoiceBundleFrame{}, false
	}
	return previous, true
}

// maxVoiceSequenceJump is the forward Sequence step always accepted. Longer
// steps must be covered by the time since the previous packet, at one frame per
// voiceSequenceStep: real loss leaves a pause, a forged jump does not.
const (
	maxVoiceSequenceJump = 64
	voiceSequenceStep    = 10 * time.Millisecond // half of the 20 ms frame
)

// AcceptVoiceSequence reports whether a voice packet with sequence may be
// forwarded. Duplicates and reordered packets pass; a step back further than
// maxVoiceSequenceJump is a sender restart and resets the reference.
func (h *Hub) AcceptVoiceSequence(id uint64, sequence uint32) bool {
	return h.acceptVoiceSequenceAt(id, sequence, time.Now())
}

func (h *Hub) acceptVoiceSequenceAt(id uint64, sequence uint32, at time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[id]
	if s == nil {
		return false
	}
	if s.voiceSequenceAt.IsZero() {
		s.voiceSequence, s.voiceSequenceAt = sequence, at
		return true
	}
	step := int64(int32(sequence - s.voiceSequence))
	switch {
	case step <= 0 && step >= -maxVoiceSequenceJump:
		return true
	case step < 0:
		s.voiceSequence, s.voiceSequenceAt = sequence, at
		return true
	}
	if step > maxVoiceSequenceJump+int64(at.Sub(s.voiceSequenceAt)/voiceSequenceStep) {
		return false
	}
	s.voiceSequence, s.voiceSequenceAt = sequence, at
	return true
}

func (h *Hub) RecordVoicePacket(id uint64, sequence uint32) {
	h.recordVoicePacketAt(id, sequence, time.Now())
}

// Keep samples slightly beyond the client window to cover heartbeat transit.
const voiceStatsWindow = 35 * time.Second
const maxVoiceStatsPackets = 4096

func (h *Hub) recordVoicePacketAt(id uint64, sequence uint32, at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.sessions[id]
	if s == nil {
		return
	}
	trimVoiceArrivals(s, at)
	if s.voiceSeen == nil {
		s.voiceSeen = make(map[uint32]struct{})
	}
	if _, duplicate := s.voiceSeen[sequence]; duplicate {
		return
	}
	s.voiceSeen[sequence] = struct{}{}
	s.voiceArrivals = append(s.voiceArrivals, voiceSample{sequence: sequence, at: at})
	trimVoiceArrivals(s, at)
}

func trimVoiceArrivals(s *Session, now time.Time) {
	cutoff := now.Add(-voiceStatsWindow)
	i := 0
	for i < len(s.voiceArrivals) && s.voiceArrivals[i].at.Before(cutoff) {
		delete(s.voiceSeen, s.voiceArrivals[i].sequence)
		i++
	}
	s.voiceArrivals = s.voiceArrivals[i:]
	if len(s.voiceArrivals) > maxVoiceStatsPackets {
		trim := len(s.voiceArrivals) - maxVoiceStatsPackets
		for _, sample := range s.voiceArrivals[:trim] {
			delete(s.voiceSeen, sample.sequence)
		}
		s.voiceArrivals = s.voiceArrivals[trim:]
	}
}

func (h *Hub) VoiceReceivedWindow(id uint64, now time.Time, end, count uint16) uint32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if s := h.sessions[id]; s != nil {
		trimVoiceArrivals(s, now)
		var received uint32
		for _, sample := range s.voiceArrivals {
			if uint16(end-uint16(sample.sequence)) < count {
				received++
			}
		}
		return received
	}
	return 0
}

func (h *Hub) JoinChannel(id uint64, channelID domain.ChannelID) error {
	_, err := h.JoinChannelWithRevision(id, channelID)
	return err
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

func (h *Hub) JoinChannelWithRevision(id uint64, channelID domain.ChannelID) (domain.StateRevision, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if err := h.joinChannelLocked(id, channelID); err != nil {
		return 0, err
	}
	return h.revision, nil
}

func (h *Hub) joinChannelLocked(id uint64, channelID domain.ChannelID) error {
	session, ok := h.sessions[id]
	if !ok {
		return ErrSessionNotFound
	}
	channel, ok := h.channels[channelID]
	if !ok {
		return fmt.Errorf("%w: %d", ErrChannelNotFound, channelID)
	}
	if session.ChannelID == channelID {
		return nil
	}
	if session.JoinLevel < channel.MinJoinLevel {
		return ErrChannelForbidden
	}
	if channel.MaxUsers > 0 && h.channelMemberCountLocked(channelID) >= channel.MaxUsers {
		return fmt.Errorf("%w: %q", ErrChannelFull, channel.Name)
	}
	if h.channelMemberCountLocked(channelID) >= protocol.MaxVoiceSources {
		return fmt.Errorf("%w: voice source limit %d", ErrChannelFull, protocol.MaxVoiceSources)
	}
	if err := h.advanceVoiceIdentityLocked(session, true, true); err != nil {
		return err
	}

	h.stopScreenShareByOwnerLocked(id)
	session.ChannelID = channelID
	h.revision++
	if session.voiceControl != nil {
		session.voiceControl.profileRevision = uint64(h.revision)
	}
	h.emitLocked(domain.StateEvent{Kind: domain.ParticipantMoved, SessionID: id, ChannelID: channelID})
	return nil
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

func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return len(h.sessions)
}

func (h *Hub) Revision() domain.StateRevision {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.revision
}

// SetMediaPort publishes the HTTPS signaling port in the initial snapshot.
// It is intended for server startup, before accepting clients.
func (h *Hub) SetMediaPort(port uint16) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.serverInfo.MediaPort == port {
		return
	}
	h.serverInfo.MediaPort = port
	h.revision++
}

func (h *Hub) CreateChannel(channel domain.Channel) (domain.Channel, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if channel.ID != 0 {
		return domain.Channel{}, fmt.Errorf(
			"%w: channel ID is assigned by the hub",
			ErrInvalidChannel,
		)
	}
	if channel.Type == 0 {
		channel.Type = domain.ChannelTypePermanent
	}
	if channel.Audio == (domain.AudioProfile{}) {
		channel.Audio = domain.DefaultAudioProfile()
	}
	if err := validateChannel(channel); err != nil {
		return domain.Channel{}, err
	}
	if err := h.validateChannelParentLocked(channel.ParentID); err != nil {
		return domain.Channel{}, err
	}
	if h.channelNameExistsLocked(channel.ParentID, channel.Name) {
		return domain.Channel{}, fmt.Errorf(
			"%w: %q",
			ErrChannelNameTaken,
			channel.Name,
		)
	}

	id, err := h.nextChannelIDLocked()
	if err != nil {
		return domain.Channel{}, err
	}
	channel.ID = id
	h.channels[id] = cloneChannel(channel)
	h.revision++
	return channel, nil
}

// RestoreChannel inserts a persisted channel with its original ID at startup.
// Parents must be restored before children.
func (h *Hub) RestoreChannel(channel domain.Channel) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if channel.ID == 0 || channel.Type != domain.ChannelTypePermanent {
		return ErrInvalidChannel
	}
	if _, exists := h.channels[channel.ID]; exists {
		return ErrInvalidChannel
	}
	if err := validateChannel(channel); err != nil {
		return err
	}
	if err := h.validateChannelParentLocked(channel.ParentID); err != nil {
		return err
	}
	if h.channelNameExistsLocked(channel.ParentID, channel.Name) {
		return ErrChannelNameTaken
	}
	h.channels[channel.ID] = cloneChannel(channel)
	if channel.ID >= h.nextChannelID {
		h.nextChannelID = channel.ID + 1
	}
	h.revision++
	return nil
}

// ApplyJoinLevel changes the effective level of every active session for an
// account. Sessions that no longer qualify for their channel are disconnected.
func (h *Hub) ApplyJoinLevel(userID int64, level uint16) []uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	var removed []uint64
	for id, session := range h.sessions {
		if session.UserID != userID {
			continue
		}
		session.JoinLevel = level
		if channel := h.channels[session.ChannelID]; channel != nil && level < channel.MinJoinLevel {
			delete(h.sessions, id)
			h.stopScreenShareByOwnerLocked(id)
			h.revision++
			h.emitLocked(domain.StateEvent{Kind: domain.ParticipantLeft, SessionID: id})
			removed = append(removed, id)
		}
	}
	sort.Slice(removed, func(i, j int) bool { return removed[i] < removed[j] })
	return removed
}

func (h *Hub) SetChannelJoinLevel(channelID domain.ChannelID, level uint16) ([]uint64, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	channel := h.channels[channelID]
	if channel == nil {
		return nil, ErrChannelNotFound
	}
	if channelID == h.serverInfo.DefaultChannelID && level != 0 {
		return nil, fmt.Errorf("%w: default channel must have join level 0", ErrInvalidChannel)
	}
	if channel.MinJoinLevel == level {
		return nil, nil
	}
	channel.MinJoinLevel = level
	h.revision++
	h.emitLocked(domain.StateEvent{Kind: domain.ChannelUpdated, Channel: *cloneChannel(*channel)})
	return h.removeBelowJoinLevelLocked(channelID, level), nil
}

// removeBelowJoinLevelLocked disconnects the sessions in channelID whose
// level no longer reaches level.
func (h *Hub) removeBelowJoinLevelLocked(channelID domain.ChannelID, level uint16) []uint64 {
	var removed []uint64
	for id, session := range h.sessions {
		if session.ChannelID != channelID || session.JoinLevel >= level {
			continue
		}
		delete(h.sessions, id)
		h.stopScreenShareByOwnerLocked(id)
		h.revision++
		h.emitLocked(domain.StateEvent{Kind: domain.ParticipantLeft, SessionID: id})
		removed = append(removed, id)
	}
	sort.Slice(removed, func(i, j int) bool { return removed[i] < removed[j] })
	return removed
}

func (h *Hub) GetChannel(id domain.ChannelID) (domain.Channel, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	channel, ok := h.channels[id]
	if !ok {
		return domain.Channel{}, false
	}
	return *cloneChannel(*channel), true
}

func (h *Hub) ListChannels() []domain.Channel {
	h.mu.RLock()
	defer h.mu.RUnlock()

	return h.channelsSnapshotLocked()
}

func (h *Hub) Inspect() OperationalSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()

	sessions := make([]Session, 0, len(h.sessions))
	for _, session := range h.sessions {
		sessions = append(sessions, *cloneSession(session))
	}
	sortSessionsByID(sessions)

	return OperationalSnapshot{VoiceControlBudgetDeferrals: h.voiceStateDeferrals,
		Revision:   h.revision,
		ServerInfo: h.serverInfo,
		Channels:   h.channelsSnapshotLocked(),
		Sessions:   sessions,
	}
}

func (h *Hub) ClientSnapshot() domain.ServerSnapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	participants := make([]domain.Participant, 0, len(h.sessions))
	for _, session := range h.sessions {
		participants = append(participants, participantFromSession(session))
	}
	sort.Slice(participants, func(i, j int) bool { return participants[i].SessionID < participants[j].SessionID })
	streams := h.screenStreamsSnapshotLocked()
	return domain.ServerSnapshot{Revision: h.revision, Info: h.serverInfo, Channels: h.channelsSnapshotLocked(), Participants: participants, ScreenStreams: streams}
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

func (h *Hub) Members(channelID domain.ChannelID) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	members := make([]string, 0)
	for _, session := range h.sessions {
		if session.ChannelID == channelID {
			members = append(members, session.Name)
		}
	}
	sort.Strings(members)
	return members
}

func (h *Hub) SessionsInChannel(channelID domain.ChannelID) []Session {
	h.mu.RLock()
	defer h.mu.RUnlock()

	sessions := make([]Session, 0)
	for _, session := range h.sessions {
		if session.ChannelID == channelID {
			sessions = append(sessions, *cloneSession(session))
		}
	}
	sortSessionsByID(sessions)
	return sessions
}

func (h *Hub) Recipients(channelID domain.ChannelID, senderID uint64) []Session {
	h.mu.RLock()
	defer h.mu.RUnlock()

	sessions := make([]Session, 0)
	for _, session := range h.sessions {
		if session.ChannelID == channelID && session.ID != senderID {
			sessions = append(sessions, *cloneSession(session))
		}
	}
	sortSessionsByID(sessions)
	return sessions
}

func (h *Hub) RecipientsFor(senderID uint64) ([]Session, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	sender, ok := h.sessions[senderID]
	if !ok {
		return nil, fmt.Errorf("session %d not found", senderID)
	}
	if sender.ChannelID == 0 {
		return nil, fmt.Errorf("session %d: %w", senderID, ErrSessionNotInChannel)
	}

	recipients := make([]Session, 0)
	for _, session := range h.sessions {
		if session.ChannelID == sender.ChannelID && session.ID != senderID {
			recipients = append(recipients, *cloneSession(session))
		}
	}
	sortSessionsByID(recipients)
	return recipients, nil
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

func validateChannel(channel domain.Channel) error {
	if !validChannelName(channel.Name) {
		return fmt.Errorf(
			"%w: channel name must be valid UTF-8, trimmed, and 1..%d bytes",
			ErrInvalidChannel,
			domain.MaxChannelNameBytes,
		)
	}
	if !utf8.ValidString(channel.Topic) || len(channel.Topic) > domain.MaxChannelTopicBytes {
		return fmt.Errorf(
			"%w: channel topic exceeds %d bytes or is not valid UTF-8",
			ErrInvalidChannel,
			domain.MaxChannelTopicBytes,
		)
	}
	if !utf8.ValidString(channel.Description) ||
		len(channel.Description) > domain.MaxChannelDescriptionBytes {
		return fmt.Errorf(
			"%w: channel description exceeds %d bytes or is not valid UTF-8",
			ErrInvalidChannel,
			domain.MaxChannelDescriptionBytes,
		)
	}
	if channel.Type != domain.ChannelTypePermanent {
		return fmt.Errorf("%w: unsupported channel type %d", ErrInvalidChannel, channel.Type)
	}
	if err := domain.ValidateAudioProfile(channel.Audio); err != nil {
		return fmt.Errorf("%w: unsupported audio profile: %v", ErrInvalidChannel, err)
	}
	return nil
}

func validChannelName(name string) bool {
	return utf8.ValidString(name) &&
		!hasNameControlCharacters(name) &&
		name != "" &&
		len(name) <= domain.MaxChannelNameBytes &&
		strings.TrimSpace(name) == name
}

// hasNameControlCharacters reports control and format (Cf) characters. Bidi
// overrides and zero-width marks could make one name pass for another.
func hasNameControlCharacters(name string) bool {
	return strings.ContainsFunc(name, func(r rune) bool {
		return unicode.IsControl(r) || r == '\u2028' || r == '\u2029' || unicode.Is(unicode.Cf, r)
	})
}

func (h *Hub) validateChannelParentLocked(parentID domain.ChannelID) error {
	depth := 1
	seen := make(map[domain.ChannelID]struct{})
	for parentID != 0 {
		if _, duplicate := seen[parentID]; duplicate {
			return fmt.Errorf("%w: channel hierarchy contains a cycle", ErrInvalidChannel)
		}
		seen[parentID] = struct{}{}

		parent, ok := h.channels[parentID]
		if !ok {
			return fmt.Errorf("%w: parent %d", ErrChannelNotFound, parentID)
		}
		depth++
		if depth > domain.MaxChannelDepth {
			return fmt.Errorf(
				"%w: channel depth exceeds %d",
				ErrInvalidChannel,
				domain.MaxChannelDepth,
			)
		}
		parentID = parent.ParentID
	}
	return nil
}

func (h *Hub) channelNameExistsLocked(parentID domain.ChannelID, name string) bool {
	for _, channel := range h.channels {
		if channel.ParentID == parentID && strings.EqualFold(channel.Name, name) {
			return true
		}
	}
	return false
}

func (h *Hub) channelMemberCountLocked(channelID domain.ChannelID) uint32 {
	var count uint32
	for _, session := range h.sessions {
		if session.ChannelID == channelID {
			count++
		}
	}
	return count
}

func (h *Hub) nextChannelIDLocked() (domain.ChannelID, error) {
	for h.nextChannelID != 0 {
		id := h.nextChannelID
		h.nextChannelID++
		if _, exists := h.channels[id]; !exists {
			return id, nil
		}
	}
	return 0, errors.New("channel ID space exhausted")
}

func cloneChannel(channel domain.Channel) *domain.Channel {
	clone := channel
	return &clone
}

func (h *Hub) channelsSnapshotLocked() []domain.Channel {
	channels := make([]domain.Channel, 0, len(h.channels))
	for _, channel := range h.channels {
		channels = append(channels, *cloneChannel(*channel))
	}
	sort.Slice(channels, func(i int, j int) bool {
		if channels[i].ParentID != channels[j].ParentID {
			return channels[i].ParentID < channels[j].ParentID
		}
		if channels[i].Position != channels[j].Position {
			return channels[i].Position < channels[j].Position
		}
		return channels[i].ID < channels[j].ID
	})
	return channels
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
