package voice

import (
	"net"

	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func HandleVoicePacket(
	conn *udp.ServerPacketConn,
	hub *Hub,
	packet protocol.VoicePacket,
	addr *net.UDPAddr,
) error {
	return handleVoiceV2(conn, hub, packet, addr)
}

func FindRecipients(hub *Hub, packet protocol.VoicePacket) ([]Session, error) {
	return hub.RecipientsFor(packet.SessionID)
}
