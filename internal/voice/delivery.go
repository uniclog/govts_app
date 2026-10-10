package voice

import (
	"errors"
	"fmt"
	"net"

	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

var ErrInvalidSessionAddr = errors.New("invalid session address")

func SendToSession(
	conn *udp.ServerPacketConn,
	session Session,
	packet protocol.VoicePacket,
) error {
	addr := session.Addr
	if addr == nil {
		return fmt.Errorf("session %d has no UDP address", session.ID)
	}
	return conn.WritePacket(session.ID, addr, packet)
}

func SendToSessions(
	conn *udp.ServerPacketConn,
	sessions []Session,
	packet protocol.VoicePacket,
) error {
	for _, session := range sessions {
		if err := SendToSession(conn, session, packet); err != nil {
			return err
		}
	}
	return nil
}

func UpdateSessionAddr(hub *Hub, sessionID uint64, addr *net.UDPAddr) error {
	return hub.UpdateAddr(sessionID, addr)
}

func ValidateSessionAddr(hub *Hub, sessionID uint64, addr *net.UDPAddr) error {
	if addr == nil {
		return errors.New("client UDP address is required")
	}

	session, ok := hub.Get(sessionID)
	if !ok {
		return fmt.Errorf("%w: %d", ErrSessionNotFound, sessionID)
	}
	if session.Addr == nil ||
		!session.Addr.IP.Equal(addr.IP) ||
		session.Addr.Port != addr.Port {
		return fmt.Errorf("%w: ip=%s port=%d", ErrInvalidSessionAddr, addr.IP, addr.Port)
	}
	return nil
}
