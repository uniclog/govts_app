package voice

import (
	"net"
	"time"

	"uniclog.io/sonoryx/internal/protocol"
)

func HandleVoiceStateControl(hub *Hub, packet protocol.VoicePacket, addr *net.UDPAddr, now time.Time) error {
	if err := ValidateSessionAddr(hub, packet.SessionID, addr); err != nil {
		return err
	}
	if packet.RequestID != 0 || packet.Sequence != 0 {
		return protocol.ErrInvalidVoiceState
	}
	switch packet.Type {
	case protocol.PacketVoiceStateRequest:
		if _, err := protocol.DecodeVoiceStateRequest(packet.Payload); err != nil {
			return err
		}
		return hub.RequestVoiceState(packet.SessionID, now)
	case protocol.PacketVoiceStateAck:
		revision, err := protocol.DecodeVoiceStateAck(packet.Payload)
		if err != nil {
			return err
		}
		// Old/duplicate acknowledgements are harmless and must not mutate the
		// active generation or extend heartbeat lifetime.
		hub.AckVoiceState(packet.SessionID, revision)
		return nil
	default:
		return protocol.ErrInvalidVoiceState
	}
}
