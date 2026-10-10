package client

import (
	"context"
	"encoding/binary"
	"errors"
	"time"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func Moderate(ctx context.Context, conn *udp.ClientPacketConn, state *State, action uint8, targetID uint64, channelID domain.ChannelID) error {
	if action != protocol.PacketKick && action != protocol.PacketBan && action != protocol.PacketDrag {
		return errors.New("unsupported moderation action")
	}
	if targetID == 0 || (targetID == state.SessionID() && action != protocol.PacketDrag) {
		return errors.New("moderation requires another participant")
	}
	payload := binary.BigEndian.AppendUint64(nil, targetID)
	if action == protocol.PacketDrag {
		if channelID == 0 {
			return errors.New("drag requires a channel")
		}
		payload = binary.BigEndian.AppendUint64(payload, uint64(channelID))
	}
	response, err := DoRequest(ctx, conn, state, protocol.VoicePacket{Type: action, Payload: payload}, 3*time.Second)
	if err != nil {
		return err
	}
	if response.Type != protocol.PacketModerationAck {
		return errors.New("unexpected moderation response")
	}
	return nil
}
