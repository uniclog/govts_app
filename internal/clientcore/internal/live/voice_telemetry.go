package client

import (
	"context"
	"time"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/voiceflow"
)

func (s *State) VoiceEndpoint() *voiceflow.Endpoint {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.voiceFlow
}
func (s *State) SetVoiceReserve(bytesPerSecond int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if bytesPerSecond <= 0 {
		bytesPerSecond = 128 * 1024
	}
	s.voiceReserve = bytesPerSecond
	s.voiceFlow.SetBudget(bytesPerSecond)
}
func (s *State) AcceptVoiceTelemetry(generation uint64, p protocol.VoicePacket, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != generation || s.sessionID != p.SessionID {
		return nil
	}
	if p.Type == protocol.PacketVoiceSummary {
		v, err := protocol.DecodeVoiceSummary(p.Payload)
		if err != nil {
			return err
		}
		if err := s.voiceFlow.AcceptSummary(v, now); err != nil {
			return err
		}
		// Keep the audio horizon moving during complete voice loss as well.
		// A valid summary never lowers the installed receiver barrier.
		if s.voice != nil {
			for _, interval := range v.Intervals {
				if interval.SuccessfulSends+interval.LocalSendFailures > 0 && int32(interval.LastSequence-s.voice.datagramFloor) > 65536 {
					s.voice.datagramFloor = interval.LastSequence - 65536
				}
			}
		}
		return nil
	}
	v, err := protocol.DecodeVoiceReport(p.Payload)
	if err != nil {
		return err
	}
	return s.voiceFlow.AcceptReport(v, now)
}
func (s *State) RecordVoiceRTT(generation uint64, id uint32, duration time.Duration, now time.Time) {
	if duration <= 0 || duration > 10*time.Second {
		return
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.generation != generation {
		return
	}
	s.voiceFlow.RTT(protocol.VoiceRTTSample{ID: id, Micros: uint32(max(1, duration.Microseconds())), Valid: true}, now)
}
func VoiceTelemetryLoop(ctx context.Context, conn VoicePacketSender, state *State) error {
	generation, id := state.SessionIdentity()
	endpoint := state.VoiceEndpoint()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C:
			if g, sid := state.SessionIdentity(); g != generation || sid != id {
				return ErrConnectionLost
			}
			summary, report := endpoint.Tick(now)
			var packets []protocol.VoicePacket
			if summary != nil {
				payload, err := protocol.EncodeVoiceSummary(*summary)
				if err != nil {
					return err
				}
				packets = append(packets, protocol.VoicePacket{Type: protocol.PacketVoiceSummary, SessionID: id, Payload: payload})
			}
			if report != nil {
				payload, err := protocol.EncodeVoiceReport(*report)
				if err != nil {
					return err
				}
				packets = append(packets, protocol.VoicePacket{Type: protocol.PacketVoiceReport, SessionID: id, Payload: payload})
			}
			for _, packet := range packets {
				if err := conn.SendPacket(packet); err != nil {
					return err
				}
			}
		}
	}
}

func (s *State) VoiceReserve() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.voiceReserve <= 0 {
		return 128 * 1024
	}
	return s.voiceReserve
}

func (s *State) RecordVoiceProtocolDrop(generation uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.generation == generation {
		s.voiceFlow.ProtocolDrop()
	}
}
