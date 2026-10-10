package client

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"uniclog.io/sonoryx/internal/domain"
)

type State struct {
	mu                 sync.RWMutex
	syncMu             sync.Mutex
	syncSerial         uint64
	syncedGeneration   uint64
	observedRevision   domain.StateRevision
	resync             chan struct{}
	subscribers        map[chan struct{}]struct{}
	notices            chan string
	notificationSounds chan NotificationSound
	speaking           map[uint64]time.Time
	Audio              *AudioControlState
	measurements       connectionMeasurements
	chatRevision       uint64

	sessionID        uint64
	joinLevel        uint16
	permissions      uint8
	name             string
	channelID        domain.ChannelID
	snapshot         domain.ServerSnapshot
	generation       uint64
	status           ConnectionStatus
	snapshotFresh    bool
	lastHeartbeatAck time.Time

	nextRequestID atomic.Uint32
	pending       map[uint32]chan ControlResponse
}

type ConnectionStatus uint8

const (
	ConnectionConnecting ConnectionStatus = iota + 1
	ConnectionConnected
	ConnectionReconnecting
	ConnectionDisconnected
)

type ControlResponse struct {
	Type      uint8
	RequestID uint32
	Sequence  uint32
	Payload   []byte
}

func NewState(sessionID uint64, name string) *State {
	s := &State{
		sessionID:          sessionID,
		name:               name,
		generation:         1,
		status:             ConnectionConnecting,
		pending:            make(map[uint32]chan ControlResponse),
		resync:             make(chan struct{}, 1),
		subscribers:        make(map[chan struct{}]struct{}),
		notices:            make(chan string, 64),
		notificationSounds: make(chan NotificationSound, 4),
		speaking:           make(map[uint64]time.Time),
	}
	s.Audio = NewAudioControlState(func() { s.audioChanged() })
	return s
}

func (s *State) ChannelID() domain.ChannelID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.channelID
}

func (s *State) SetChannelID(channelID domain.ChannelID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.channelID != channelID {
		s.measurements.resetIncoming()
	}
	s.channelID = channelID
	s.notifyLocked()
}

func (s *State) Generation() uint64 { s.mu.RLock(); defer s.mu.RUnlock(); return s.generation }
func (s *State) SessionIdentity() (uint64, uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.generation, s.sessionID
}
func (s *State) ConnectionStatus() ConnectionStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}
func (s *State) SnapshotFresh() bool { s.mu.RLock(); defer s.mu.RUnlock(); return s.snapshotFresh }

func (s *State) LastHeartbeatAck() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastHeartbeatAck
}

func (s *State) MarkHeartbeatAck(at time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastHeartbeatAck = at
}

func (s *State) SetConnectionStatus(status ConnectionStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = status
	s.notifyLocked()
}

// PrepareConnection clears state owned by a previous server while preserving
// application-lifetime audio controls and view subscriptions.
func (s *State) PrepareConnection() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) != 0 {
		return errors.New("cannot prepare connection with pending requests")
	}
	s.generation++
	s.syncedGeneration = 0
	s.observedRevision = 0
	s.sessionID = 0
	s.joinLevel = 0
	s.permissions = 0
	s.channelID = 0
	s.snapshot = domain.ServerSnapshot{}
	s.status = ConnectionConnecting
	s.snapshotFresh = false
	s.lastHeartbeatAck = time.Time{}
	s.measurements.reset()
	clear(s.speaking)
	s.Audio.ClearParticipantVolumes()
	s.clearNotificationSoundsLocked()
	s.notifyLocked()
	return nil
}

func (s *State) InvalidateSession(status ConnectionStatus) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation++
	s.status = status
	s.snapshotFresh = false
	s.lastHeartbeatAck = time.Time{}
	s.measurements.reset()
	s.channelID = 0
	s.sessionID = 0
	s.joinLevel = 0
	s.permissions = 0
	s.observedRevision = 0
drainNotices:
	for {
		select {
		case <-s.notices:
		default:
			break drainNotices
		}
	}
	clear(s.speaking)
	s.Audio.ClearParticipantVolumes()
	s.clearNotificationSoundsLocked()
	s.notifyLocked()
	return s.generation
}

func (s *State) clearNotificationSoundsLocked() {
	for {
		select {
		case <-s.notificationSounds:
		default:
			return
		}
	}
}

func (s *State) StartSession(sessionID uint64) (uint64, error) {
	if sessionID == 0 {
		return 0, errors.New("session ID must not be zero")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) != 0 {
		return 0, errors.New("cannot start session with pending requests")
	}
	s.generation++
	s.sessionID = sessionID
	s.joinLevel = 0
	s.permissions = 0
	s.channelID = 0
	s.snapshotFresh = false
	s.lastHeartbeatAck = time.Time{}
	s.measurements.reset()
	s.status = ConnectionConnecting
	s.observedRevision = 0
	clear(s.speaking)
	s.notifyLocked()
	return s.generation, nil
}

func (s *State) SetAccountPrivileges(level uint16, permissions uint8) {
	s.mu.Lock()
	s.joinLevel = level
	s.permissions = permissions
	s.notifyLocked()
	s.mu.Unlock()
}

func (s *State) SetChannelIDForGeneration(generation uint64, channelID domain.ChannelID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != generation {
		return false
	}
	if s.channelID != channelID {
		s.measurements.resetIncoming()
	}
	s.channelID = channelID
	clear(s.speaking)
	s.notifyLocked()
	return true
}

func (s *State) Snapshot() domain.ServerSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot.Clone()
}
func (s *State) ReplaceSnapshot(snapshot domain.ServerSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetIncomingForSnapshotLocked(snapshot)
	s.snapshot = snapshot.Clone()
	s.snapshotFresh = true
	s.syncedGeneration = s.generation
	s.notifyLocked()
}

func (s *State) ReplaceSnapshotForGeneration(generation uint64, snapshot domain.ServerSnapshot) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != generation {
		return false
	}
	if s.syncedGeneration == generation && snapshot.Revision < s.snapshot.Revision {
		return true
	}
	if snapshot.Revision < s.observedRevision {
		s.requestResyncLocked()
		return true
	}
	s.resetIncomingForSnapshotLocked(snapshot)
	s.snapshot = snapshot.Clone()
	s.syncedGeneration = generation
	s.snapshotFresh = snapshot.Revision >= s.observedRevision
	clear(s.speaking)
	for _, p := range snapshot.Participants {
		if p.SessionID == s.sessionID {
			s.channelID = p.ChannelID
		}
	}
	if !s.snapshotFresh {
		s.requestResyncLocked()
	}
	s.notifyLocked()
	return true
}

// resetIncomingForSnapshotLocked removes sequence baselines that no longer
// describe packets forwarded to this client's current channel.
func (s *State) resetIncomingForSnapshotLocked(next domain.ServerSnapshot) {
	localChannel := s.channelID
	for _, p := range next.Participants {
		if p.SessionID == s.sessionID {
			localChannel = p.ChannelID
			break
		}
	}
	if localChannel != s.channelID {
		s.measurements.resetIncoming()
		return
	}
	oldChannels := make(map[uint64]domain.ChannelID, len(s.snapshot.Participants))
	for _, p := range s.snapshot.Participants {
		oldChannels[p.SessionID] = p.ChannelID
	}
	for _, p := range next.Participants {
		if old, ok := oldChannels[p.SessionID]; ok {
			if old != p.ChannelID {
				s.measurements.resetSender(p.SessionID)
			}
			delete(oldChannels, p.SessionID)
		}
	}
	for id := range oldChannels {
		s.measurements.resetSender(id)
	}
}

func (s *State) SessionID() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sessionID
}

func (s *State) RegisterRequest(requestID uint32) <-chan ControlResponse {
	s.mu.Lock()
	defer s.mu.Unlock()

	ch := make(chan ControlResponse, 1)
	s.pending[requestID] = ch
	return ch
}

func (s *State) CompleteRequest(response ControlResponse) bool {
	s.mu.Lock()
	ch, ok := s.pending[response.RequestID]
	if ok {
		delete(s.pending, response.RequestID)
	}
	s.mu.Unlock()

	if !ok {
		return false
	}

	ch <- response
	close(ch)
	return true
}

func (s *State) CancelRequest(requestID uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, requestID)
}

func (s *State) NextRequestID() uint32 {
	for {
		requestID := s.nextRequestID.Add(1)
		if requestID != 0 {
			return requestID
		}
	}
}
