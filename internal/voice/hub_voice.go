package voice

import (
	"time"

	"uniclog.io/sonoryx/internal/protocol"
)

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
