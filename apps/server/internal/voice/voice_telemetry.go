package voice

import (
	"net"
	"time"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/voiceflow"
)

func (h *Hub) SetVoiceControlRates(recipient, global int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.voiceStateRecipientRate = max(1, recipient)
	h.voiceStateGlobalRate = max(1, global)
}

func (h *Hub) voiceEndpointLocked(s *Session, now time.Time) *voiceflow.Endpoint {
	if h.voicePool == nil {
		h.voicePool = voiceflow.NewPool(8 * 1024 * 1024)
	}
	if s.voiceFlow == nil {
		s.voiceFlow = voiceflow.NewEndpoint(protocol.VoiceDownstream, now)
		s.voiceFlow.SetEnabled(h.voiceBundles)
		if h.voiceReserve > 0 {
			s.voiceFlow.SetBudget(h.voiceReserve)
		}
	}
	return s.voiceFlow
}
func (h *Hub) SetVoiceReserves(recipient, global int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if recipient <= 0 {
		recipient = 128 * 1024
	}
	if global <= 0 {
		global = 8 * 1024 * 1024
	}
	h.voiceReserve = recipient
	h.voicePool = voiceflow.NewPool(global)
	for _, s := range h.sessions {
		if s.voiceFlow != nil {
			s.voiceFlow.SetBudget(recipient)
		}
	}
}

func HandleVoiceTelemetry(h *Hub, p protocol.VoicePacket, addr *net.UDPAddr, now time.Time) error {
	if err := ValidateSessionAddr(h, p.SessionID, addr); err != nil {
		return err
	}
	if p.RequestID != 0 || p.Sequence != 0 {
		return voiceflow.ErrTelemetry
	}
	h.mu.Lock()
	s := h.sessions[p.SessionID]
	if s == nil {
		h.mu.Unlock()
		return ErrSessionNotFound
	}
	flow := h.voiceEndpointLocked(s, now)
	h.mu.Unlock()
	if p.Type == protocol.PacketVoiceSummary {
		v, err := protocol.DecodeVoiceSummary(p.Payload)
		if err != nil {
			return err
		}
		return flow.AcceptSummary(v, now)
	}
	v, err := protocol.DecodeVoiceReport(p.Payload)
	if err != nil {
		return err
	}
	return flow.AcceptReport(v, now)
}
func (h *Hub) DueVoiceTelemetry(now time.Time) []VoiceStateDelivery {
	h.mu.Lock()
	defer h.mu.Unlock()
	var deliveries []VoiceStateDelivery
	for _, s := range h.sessions {
		if s.voiceControl == nil {
			continue
		}
		flow := h.voiceEndpointLocked(s, now)
		summary, report := flow.Tick(now)
		delivery := VoiceStateDelivery{Session: *cloneSession(s)}
		if summary != nil {
			if payload, err := protocol.EncodeVoiceSummary(*summary); err == nil {
				delivery.Packets = append(delivery.Packets, protocol.VoicePacket{Type: protocol.PacketVoiceSummary, SessionID: s.ID, Payload: payload})
			}
		}
		if report != nil {
			if payload, err := protocol.EncodeVoiceReport(*report); err == nil {
				delivery.Packets = append(delivery.Packets, protocol.VoicePacket{Type: protocol.PacketVoiceReport, SessionID: s.ID, Payload: payload})
			}
		}
		if len(delivery.Packets) > 0 {
			deliveries = append(deliveries, delivery)
		}
	}
	return deliveries
}
