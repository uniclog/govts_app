package client

import (
	"context"
	"errors"

	"uniclog.io/govts/internal/protocol"
	"uniclog.io/govts/internal/transport/udp"
)

func AudioStateLoop(ctx context.Context, conn *udp.ClientPacketConn, state *State) error {
	changed, unsubscribe := state.Subscribe(ctx)
	defer unsubscribe()
	var published bool
	var muted, deafened bool
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-changed:
			if !ok {
				return nil
			}
			nextMuted, nextDeafened, _ := state.Audio.Snapshot()
			if published && nextMuted == muted && nextDeafened == deafened {
				continue
			}
			if err := publishAudioState(ctx, conn, state, nextMuted, nextDeafened); err != nil {
				return err
			}
			published = true
			muted, deafened = nextMuted, nextDeafened
		}
	}
}

func publishAudioState(ctx context.Context, conn *udp.ClientPacketConn, state *State, muted, deafened bool) error {
	response, err := DoRequest(ctx, conn, state, protocol.VoicePacket{
		Type:    protocol.PacketAudioState,
		Payload: protocol.EncodeAudioState(muted, deafened),
	}, joinRequestTimeout)
	if err != nil {
		return err
	}
	if response.Type != protocol.PacketAudioStateAck {
		return errors.New("invalid audio state response")
	}
	gotMuted, gotDeafened, err := protocol.DecodeAudioState(response.Payload)
	if err != nil || gotMuted != muted || gotDeafened != deafened {
		return errors.New("invalid audio state acknowledgement")
	}
	return nil
}
