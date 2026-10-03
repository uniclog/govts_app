package clientapp

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"time"

	"uniclog.io/govts/internal/protocol"
)

// ProbeServerPopulation asks a server how many clients are connected.
// The query does not open a voice session.
func ProbeServerPopulation(ctx context.Context, endpoint netip.AddrPort) (uint32, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	conn, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(endpoint))
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	payload, err := protocol.EncodePacket(protocol.VoicePacket{Type: protocol.PacketServerStatus, RequestID: 1})
	if err != nil {
		return 0, err
	}
	buffer := make([]byte, protocol.MaxWireDatagramSize)
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if err := conn.SetWriteDeadline(time.Now().Add(200 * time.Millisecond)); err != nil {
			return 0, err
		}
		if _, err := conn.Write(payload); err != nil {
			return 0, err
		}
		if err := conn.SetReadDeadline(time.Now().Add(600 * time.Millisecond)); err != nil {
			return 0, err
		}
		for {
			n, err := conn.Read(buffer)
			if err != nil {
				break
			}
			packet, err := protocol.DecodePacket(buffer[:n])
			if err != nil || packet.Type != protocol.PacketServerStatusAck || packet.RequestID != 1 || len(packet.Payload) != 4 {
				continue
			}
			return binary.BigEndian.Uint32(packet.Payload), nil
		}
	}
	return 0, errors.New("server did not answer")
}
