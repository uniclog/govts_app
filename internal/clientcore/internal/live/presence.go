package client

import (
	"context"
	"encoding/binary"
	"errors"
	"time"

	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func PublishPresence(ctx context.Context, conn *udp.ClientPacketConn, state *State, away bool) error {
	if state.ChannelID() == 0 {
		return errors.New("presence requires a channel")
	}
	response, err := DoRequest(ctx, conn, state, protocol.VoicePacket{
		Type:    protocol.PacketPresence,
		Payload: protocol.EncodePresence(away),
	}, 3*time.Second)
	if err != nil {
		return err
	}
	if response.Type != protocol.PacketPresenceAck {
		return errors.New("invalid presence response")
	}
	got, err := protocol.DecodePresence(response.Payload)
	if err != nil || got != away {
		return errors.New("invalid presence acknowledgement")
	}
	return nil
}

func Attention(ctx context.Context, conn *udp.ClientPacketConn, state *State, target uint64) error {
	if target == 0 || target == state.SessionID() {
		return errors.New("attention requires another participant")
	}
	response, err := DoRequest(ctx, conn, state, protocol.VoicePacket{
		Type:    protocol.PacketAttention,
		Payload: binary.BigEndian.AppendUint64(nil, target),
	}, 3*time.Second)
	if err != nil {
		return err
	}
	if response.Type != protocol.PacketAttentionAck || len(response.Payload) != 0 {
		return errors.New("invalid attention response")
	}
	return nil
}
