package voice

import (
	"net"

	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func RouteVoicePacket(
	conn *udp.ServerPacketConn,
	hub *Hub,
	packet protocol.VoicePacket,
) error {
	sessions, err := FindRecipients(hub, packet)
	if err != nil {
		return err
	}
	return SendToSessions(conn, sessions, packet)
}

func HandleVoicePacket(
	conn *udp.ServerPacketConn,
	hub *Hub,
	packet protocol.VoicePacket,
	addr *net.UDPAddr,
) error {
	if err := ValidateSessionAddr(hub, packet.SessionID, addr); err != nil {
		return err
	}
	if err := hub.Touch(packet.SessionID); err != nil {
		return err
	}
	hub.RecordVoicePacket(packet.SessionID, packet.Sequence)
	previous, ok := hub.SwapLastVoiceFrame(packet.SessionID, protocol.VoiceBundleFrame{
		Sequence: packet.Sequence,
		Payload:  packet.Payload,
	})
	if !ok {
		return RouteVoicePacket(conn, hub, packet)
	}
	bundle, ok := newVoiceBundlePacket(packet, previous)
	if !ok {
		return RouteVoicePacket(conn, hub, packet)
	}
	sessions, err := FindRecipients(hub, packet)
	if err != nil {
		return err
	}
	for _, session := range sessions {
		outgoing := packet
		if session.VoiceBundles {
			outgoing = bundle
		}
		if err := SendToSession(conn, session, outgoing); err != nil {
			return err
		}
	}
	return nil
}

// newVoiceBundlePacket repeats the previous frame next to the current one. It
// returns false when both frames do not fit one datagram; the current frame is
// then sent alone as a plain voice packet.
func newVoiceBundlePacket(
	packet protocol.VoicePacket,
	previous protocol.VoiceBundleFrame,
) (protocol.VoicePacket, bool) {
	frames := []protocol.VoiceBundleFrame{
		previous,
		{Sequence: packet.Sequence, Payload: packet.Payload},
	}
	if protocol.VoiceBundleSize(frames) > protocol.MaxPayloadSize {
		return protocol.VoicePacket{}, false
	}
	payload, err := protocol.EncodeVoiceBundle(frames)
	if err != nil {
		return protocol.VoicePacket{}, false
	}
	return protocol.VoicePacket{
		Type:      protocol.PacketVoiceBundle,
		SessionID: packet.SessionID,
		Sequence:  packet.Sequence,
		Payload:   payload,
	}, true
}

func FindRecipients(hub *Hub, packet protocol.VoicePacket) ([]Session, error) {
	return hub.RecipientsFor(packet.SessionID)
}
