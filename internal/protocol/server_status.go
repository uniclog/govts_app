package protocol

import (
	"encoding/binary"
	"errors"
)

const ServerStatusVersion byte = 1
const ServerStatusVersionV2 byte = 2

func IsServerStatusPacket(kind uint8) bool {
	return kind == PacketServerStatusRequest || kind == PacketServerStatusAck
}

// Status is public, sessionless and deliberately smaller than its request.
func NewServerStatusRequest(requestID uint32, nonce [16]byte) VoicePacket {
	payload := make([]byte, 32)
	payload[0] = ServerStatusVersion
	copy(payload[1:17], nonce[:])
	return VoicePacket{Type: PacketServerStatusRequest, RequestID: requestID, Payload: payload}
}

func NewServerStatusAck(requestID uint32, nonce [16]byte, count uint32) VoicePacket {
	payload := make([]byte, 21)
	payload[0] = ServerStatusVersion
	copy(payload[1:17], nonce[:])
	binary.BigEndian.PutUint32(payload[17:], count)
	return VoicePacket{Type: PacketServerStatusAck, RequestID: requestID, Payload: payload}
}

// V2 preserves the padded request size and adds the packed server version to the ack.
func NewServerStatusRequestV2(requestID uint32, nonce [16]byte) VoicePacket {
	packet := NewServerStatusRequest(requestID, nonce)
	packet.Payload[0] = ServerStatusVersionV2
	return packet
}

func NewServerStatusAckV2(requestID uint32, nonce [16]byte, count, version uint32) VoicePacket {
	packet := NewServerStatusAck(requestID, nonce, count)
	packet.Payload[0] = ServerStatusVersionV2
	packet.Payload = binary.BigEndian.AppendUint32(packet.Payload, version)
	return packet
}

// ServerStatusAckVersion returns a version only for a valid extended reply.
func ServerStatusAckVersion(packet VoicePacket) (uint32, bool) {
	if _, _, err := DecodeServerStatus(packet); err != nil || packet.Type != PacketServerStatusAck || packet.Payload[0] != ServerStatusVersionV2 {
		return 0, false
	}
	return binary.BigEndian.Uint32(packet.Payload[21:]), true
}

func DecodeServerStatus(packet VoicePacket) (nonce [16]byte, count uint32, err error) {
	invalid := errors.New("invalid public server status packet")
	if packet.SessionID != 0 || packet.Sequence != 0 || packet.RequestID == 0 {
		return nonce, 0, invalid
	}
	if len(packet.Payload) == 0 || (packet.Payload[0] != ServerStatusVersion && packet.Payload[0] != ServerStatusVersionV2) {
		return nonce, 0, invalid
	}
	switch packet.Type {
	case PacketServerStatusRequest:
		if len(packet.Payload) != 32 {
			return nonce, 0, invalid
		}
		for _, b := range packet.Payload[17:] {
			if b != 0 {
				return nonce, 0, invalid
			}
		}
	case PacketServerStatusAck:
		expected := 21
		if packet.Payload[0] == ServerStatusVersionV2 {
			expected = 25
		}
		if len(packet.Payload) != expected {
			return nonce, 0, invalid
		}
		count = binary.BigEndian.Uint32(packet.Payload[17:])
	default:
		return nonce, 0, invalid
	}
	copy(nonce[:], packet.Payload[1:17])
	return nonce, count, nil
}

func validPublicStatus(ctx DatagramContext, packet VoicePacket, direction PacketDirection) bool {
	if ctx.KeyOwnerID != 0 || ctx.Direction != direction {
		return false
	}
	expected := PacketServerStatusRequest
	if direction == DirectionServerToClient {
		expected = PacketServerStatusAck
	}
	if packet.Type != expected {
		return false
	}
	_, _, err := DecodeServerStatus(packet)
	return err == nil
}
