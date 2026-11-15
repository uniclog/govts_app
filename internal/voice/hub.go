package voice

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"uniclog.io/sonoryx/internal/appversion"
	"uniclog.io/sonoryx/internal/domain"
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

// SetMaxSessions bounds online sessions; values below 1 keep the current limit.
func (h *Hub) SetMaxSessions(limit int) {
	if limit < 1 {
		return
	}
	h.mu.Lock()
	h.maxSessions = limit
	h.mu.Unlock()
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
