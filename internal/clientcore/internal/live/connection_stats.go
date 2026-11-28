package client

import (
	"sync"
	"time"
	"uniclog.io/sonoryx/internal/voiceflow"
)

const statsWindow = 30 * time.Second
const maxStatsPackets = 4096

// maxSequenceGap is the longest forward jump counted as loss. A longer jump is
// a discontinuity: the gap is not filled, so a sender cannot inflate the
// statistics with large Sequence steps.
const maxSequenceGap = 64

type ConnectionStats struct {
	Voice                  voiceflow.Snapshot
	SessionID              uint64
	Generation             uint64
	Status                 ConnectionStatus
	PingMS                 float64
	PingVariationMS        float64
	PingAvailable          bool
	PingVariationAvailable bool
	PingSampleAtMS         int64
	IncomingLoss           float64
	IncomingKnown          bool
	IncomingSampleAtMS     int64
	OutgoingLoss           float64
	OutgoingKnown          bool
	// ConcealedLoss is the share of expected incoming frames replaced by PLC.
	// RecoveredLoss is the share of expected incoming frames restored from a
	// redundant copy in PacketVoiceBundle.
	RecoveredLoss    float64
	ConcealedLoss    float64
	LossBurstsSingle int
	LossBurstsDouble int
	LossBurstsLong   int
}

type voiceArrival struct {
	at   time.Time
	lost bool
}

// lossBurst is a run of consecutive frames the jitter buffer confirmed lost.
type lossBurst struct {
	at     time.Time
	length uint32
}

type voiceSequence struct {
	last     uint32
	started  bool
	lastSeen time.Time
	missing  map[uint32]time.Time
	// missingOrder lists missing sequences oldest first, so expiry and the
	// size bound drop from the front without scanning the map.
	missingOrder []uint32
}

// dropOldestMissing removes expired entries and keeps at most limit.
func (s *voiceSequence) dropOldestMissing(cutoff time.Time, limit int) {
	i := 0
	for i < len(s.missingOrder) {
		sequence := s.missingOrder[i]
		at, ok := s.missing[sequence]
		if ok && !at.Before(cutoff) && len(s.missingOrder)-i <= limit {
			break
		}
		if ok {
			delete(s.missing, sequence)
		}
		i++
	}
	s.missingOrder = s.missingOrder[i:]
}

type connectionMeasurements struct {
	// V2 has one transport sequence for all speakers, preserved across audio
	// transitions. Only the full session reset clears this baseline.
	datagramMode     bool
	mu               sync.Mutex
	pings            []float64
	pingAt           time.Time
	voiceAt          time.Time
	arrivals         []voiceArrival
	senders          map[uint64]*voiceSequence
	outgoingLoss     float64
	outgoingKnown    bool
	sentVoiceAt      []time.Time
	lastSentSequence uint32
	recoveredAt      []time.Time
	concealedAt      []time.Time
	lossBursts       []lossBurst
}

func (m *connectionMeasurements) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.datagramMode = false
	m.pings = nil
	m.pingAt = time.Time{}
	m.voiceAt = time.Time{}
	m.arrivals = nil
	m.senders = nil
	m.outgoingLoss = 0
	m.outgoingKnown = false
	m.sentVoiceAt = nil
	m.lastSentSequence = 0
	m.recoveredAt = nil
	m.concealedAt = nil
	m.lossBursts = nil
}

func (m *connectionMeasurements) resetIncoming() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.datagramMode {
		return
	}
	m.arrivals = nil
	m.senders = nil
	m.voiceAt = time.Time{}
	m.recoveredAt = nil
	m.concealedAt = nil
	m.lossBursts = nil
}

func (m *connectionMeasurements) resetSender(sender uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.datagramMode {
		return
	}
	delete(m.senders, sender)
}

func (m *connectionMeasurements) recordPing(elapsed time.Duration, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pings = append(m.pings, float64(elapsed)/float64(time.Millisecond))
	if len(m.pings) > 8 {
		m.pings = m.pings[len(m.pings)-8:]
	}
	m.pingAt = at
}

func (m *connectionMeasurements) recordOutgoingLoss(value float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outgoingLoss = value
	m.outgoingKnown = true
}

func (m *connectionMeasurements) clearOutgoingLoss() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.outgoingKnown = false
	m.outgoingLoss = 0
}

func (m *connectionMeasurements) recordVoiceSent(sequence uint32, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trimSent(at)
	m.sentVoiceAt = append(m.sentVoiceAt, at)
	m.lastSentSequence = sequence
	if len(m.sentVoiceAt) > maxStatsPackets {
		m.sentVoiceAt = m.sentVoiceAt[len(m.sentVoiceAt)-maxStatsPackets:]
	}
}

func (m *connectionMeasurements) sentInWindow(now time.Time) (uint16, uint16) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trimSent(now)
	return uint16(len(m.sentVoiceAt)), uint16(m.lastSentSequence)
}

func (m *connectionMeasurements) trimSent(now time.Time) {
	cutoff := now.Add(-statsWindow)
	i := 0
	for i < len(m.sentVoiceAt) && m.sentVoiceAt[i].Before(cutoff) {
		i++
	}
	m.sentVoiceAt = m.sentVoiceAt[i:]
}

func (m *connectionMeasurements) recordVoice(sender uint64, sequence uint32, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trim(at)
	m.voiceAt = at
	if m.senders == nil {
		m.senders = make(map[uint64]*voiceSequence)
	}
	stream := m.senders[sender]
	if stream == nil {
		stream = &voiceSequence{missing: make(map[uint32]time.Time)}
		m.senders[sender] = stream
	}
	if !stream.started {
		stream.started = true
		stream.last = sequence
		stream.lastSeen = at
		m.appendArrival(voiceArrival{at: at})
		return
	}
	if missingAt, ok := stream.missing[sequence]; ok {
		delete(stream.missing, sequence)
		stream.lastSeen = at
		// Gaps are recorded recently, so search from the newest arrival.
		for i := len(m.arrivals) - 1; i >= 0 && !m.arrivals[i].at.Before(missingAt); i-- {
			if m.arrivals[i].lost && m.arrivals[i].at.Equal(missingAt) {
				m.arrivals[i].lost = false
				return
			}
		}
		return
	}
	if !sequenceBefore(stream.last, sequence) {
		return // duplicate or old packet
	}
	gap := sequence - stream.last - 1
	if gap > maxSequenceGap { // discontinuity: avoid treating a restarted stream as massive loss
		clear(stream.missing)
		stream.missingOrder = stream.missingOrder[:0]
		gap = 0
	}
	for i := uint32(1); i <= gap; i++ {
		missing := stream.last + i
		stream.missing[missing] = at
		stream.missingOrder = append(stream.missingOrder, missing)
		m.appendArrival(voiceArrival{at: at, lost: true})
	}
	stream.dropOldestMissing(at.Add(-statsWindow), maxStatsPackets)
	stream.last = sequence
	stream.lastSeen = at
	m.appendArrival(voiceArrival{at: at})
}

func (m *connectionMeasurements) appendArrival(arrival voiceArrival) {
	m.arrivals = append(m.arrivals, arrival)
	if len(m.arrivals) > maxStatsPackets {
		m.arrivals = m.arrivals[len(m.arrivals)-maxStatsPackets:]
	}
}

// recordRedundant counts a redundant copy that fills a frame the primary
// datagrams lost. Raw loss keeps the frame as lost.
func (m *connectionMeasurements) recordRedundant(sender uint64, sequence uint32, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trim(at)
	stream := m.senders[sender]
	if stream == nil {
		return
	}
	if _, lost := stream.missing[sequence]; !lost {
		return
	}
	m.recoveredAt = append(m.recoveredAt, at)
	if len(m.recoveredAt) > maxStatsPackets {
		m.recoveredAt = m.recoveredAt[len(m.recoveredAt)-maxStatsPackets:]
	}
}

func (m *connectionMeasurements) recordConcealed(at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trim(at)
	m.concealedAt = append(m.concealedAt, at)
	if len(m.concealedAt) > maxStatsPackets {
		m.concealedAt = m.concealedAt[len(m.concealedAt)-maxStatsPackets:]
	}
}

func (m *connectionMeasurements) recordLossBurst(length uint32, at time.Time) {
	if length == 0 || length > maxSequenceGap { // discontinuity, as in recordVoice
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trim(at)
	m.lossBursts = append(m.lossBursts, lossBurst{at: at, length: length})
	if len(m.lossBursts) > maxStatsPackets {
		m.lossBursts = m.lossBursts[len(m.lossBursts)-maxStatsPackets:]
	}
}

func (m *connectionMeasurements) trim(now time.Time) {
	cutoff := now.Add(-statsWindow)
	i := 0
	for i < len(m.arrivals) && m.arrivals[i].at.Before(cutoff) {
		i++
	}
	m.arrivals = m.arrivals[i:]
	i = 0
	for i < len(m.recoveredAt) && m.recoveredAt[i].Before(cutoff) {
		i++
	}
	m.recoveredAt = m.recoveredAt[i:]
	i = 0
	for i < len(m.concealedAt) && m.concealedAt[i].Before(cutoff) {
		i++
	}
	m.concealedAt = m.concealedAt[i:]
	i = 0
	for i < len(m.lossBursts) && m.lossBursts[i].at.Before(cutoff) {
		i++
	}
	m.lossBursts = m.lossBursts[i:]
	for id, stream := range m.senders {
		stream.dropOldestMissing(cutoff, maxStatsPackets)
		if len(stream.missing) == 0 && stream.lastSeen.Before(cutoff) {
			delete(m.senders, id)
		}
	}
}

func (m *connectionMeasurements) snapshot(now time.Time) ConnectionStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trim(now)
	var result ConnectionStats
	result.OutgoingLoss = m.outgoingLoss
	result.OutgoingKnown = m.outgoingKnown
	if !m.pingAt.IsZero() && now.Sub(m.pingAt) <= 2*HeartbeatInterval && len(m.pings) > 0 {
		result.PingAvailable = true
		result.PingSampleAtMS = m.pingAt.UnixMilli()
		result.PingMS = m.pings[len(m.pings)-1]
		if len(m.pings) > 1 {
			result.PingVariationAvailable = true
			var sum float64
			for i := 1; i < len(m.pings); i++ {
				delta := m.pings[i] - m.pings[i-1]
				if delta < 0 {
					delta = -delta
				}
				sum += delta
			}
			result.PingVariationMS = sum / float64(len(m.pings)-1)
		}
	}
	if len(m.arrivals) > 0 {
		result.IncomingSampleAtMS = m.voiceAt.UnixMilli()
		var lost int
		for _, arrival := range m.arrivals {
			if arrival.lost {
				lost++
			}
		}
		result.IncomingKnown = true
		result.IncomingLoss = float64(lost) * 100 / float64(len(m.arrivals))
		result.RecoveredLoss = min(float64(len(m.recoveredAt))*100/float64(len(m.arrivals)), 100)
		result.ConcealedLoss = min(float64(len(m.concealedAt))*100/float64(len(m.arrivals)), 100)
	}
	for _, burst := range m.lossBursts {
		switch {
		case burst.length == 1:
			result.LossBurstsSingle++
		case burst.length == 2:
			result.LossBurstsDouble++
		default:
			result.LossBurstsLong++
		}
	}
	return result
}

func (s *State) ConnectionStats() ConnectionStats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := s.measurements.snapshot(time.Now())
	result.Voice = s.voiceFlow.Stats(time.Now())
	if s.voice != nil {
		result.IncomingKnown = result.Voice.Incoming.Known
		result.IncomingLoss = result.Voice.Incoming.Loss
		result.OutgoingKnown = result.Voice.Outgoing.Known
		result.OutgoingLoss = result.Voice.Outgoing.Loss
	}
	result.SessionID = s.sessionID
	result.Generation = s.generation
	result.Status = s.status
	if s.status != ConnectionConnected {
		result.PingAvailable = false
		result.PingVariationAvailable = false
		result.IncomingKnown = false
		result.OutgoingKnown = false
	}
	return result
}

func (s *State) RecordVoiceArrival(sender uint64, sequence uint32) {
	s.measurements.recordVoice(sender, sequence, time.Now())
}

// RecordVoiceDatagram observes v2 transport before any audio policy filter.
// Summary/report interval accounting will replace this rolling estimate.
func (s *State) RecordVoiceDatagram(generation uint64, sequence uint32, frameCounts ...byte) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.generation != generation || s.sessionID == 0 {
		return
	}
	s.voiceFlow.Receive(sequence, time.Now())
	if len(frameCounts) > 0 {
		s.voiceFlow.ReceivedFrames(frameCounts[0])
	}
	s.measurements.mu.Lock()
	s.measurements.datagramMode = true
	s.measurements.mu.Unlock()
	s.measurements.recordVoice(0, sequence, time.Now())
}

// RecordVoiceRedundant observes a redundant frame copy from PacketVoiceBundle.
func (s *State) RecordVoiceRedundant(sender uint64, sequence uint32) {
	s.measurements.recordRedundant(sender, sequence, time.Now())
}

// RecordVoiceConcealed counts one incoming frame synthesized by PLC.
func (s *State) RecordVoiceConcealed() {
	s.measurements.recordConcealed(time.Now())
}

// RecordVoiceLossBurst counts a gap the jitter buffer confirmed as lost.
func (s *State) RecordVoiceLossBurst(length uint32) {
	s.measurements.recordLossBurst(length, time.Now())
}

func (s *State) RecordVoiceSent(sequence uint32) {
	s.measurements.recordVoiceSent(sequence, time.Now())
}

func (s *State) VoiceSentInWindow() (uint16, uint16) {
	return s.measurements.sentInWindow(time.Now())
}
