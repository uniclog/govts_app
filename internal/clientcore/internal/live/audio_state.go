package client

import (
	"context"
	"errors"
	"time"

	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
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
				if errors.Is(err, errVoiceIntentSuperseded) || errors.Is(err, ErrVoiceControlStale) {
					published = false
					timer := time.NewTimer(250 * time.Millisecond)
					select {
					case <-ctx.Done():
						timer.Stop()
						return ctx.Err()
					case <-timer.C:
					}
					continue
				}
				return err
			}
			published = true
			muted, deafened = nextMuted, nextDeafened
		}
	}
}

func publishAudioState(ctx context.Context, conn *udp.ClientPacketConn, state *State, muted, deafened bool) (runErr error) {
	generation := state.Generation()
	unlock, err := state.lockVoiceCommand(ctx, generation)
	if err != nil {
		return err
	}
	defer unlock()
	capture, playback := state.voiceAudioChanges(muted, deafened)
	if err := state.beginVoiceCommand(generation, capture, playback); err != nil {
		return err
	}
	uncertain := false
	defer func() { runErr = state.finishVoiceCommand(generation, uncertain, runErr) }()
	for attempt := 0; attempt < 3; attempt++ {
		localMute, localDeafen, _ := state.Audio.Snapshot()
		if localMute != muted || localDeafen != deafened {
			return errVoiceIntentSuperseded
		}
		capture, playback := state.voiceAudioChanges(muted, deafened)
		if err := state.beginVoiceCommand(generation, capture, playback); err != nil {
			return err
		}
		expected, err := state.voiceCommandRevision(generation)
		if err != nil {
			return err
		}
		payload, err := protocol.EncodeAudioState(muted, deafened, expected)
		if err != nil {
			return err
		}
		response, err := doRequestAttempts(ctx, conn, state, protocol.VoicePacket{Type: protocol.PacketAudioState, Payload: payload}, joinRequestTimeout, requestAttempts, func() error {
			_, err := state.voiceCommandRevision(generation)
			if err == nil {
				uncertain = true
			}
			return err
		})
		if err != nil {
			var rejection *ServerRequestError
			if errors.As(err, &rejection) {
				uncertain = false
			}
			return err
		}
		if response.Type != protocol.PacketAudioStateAck {
			return errors.New("invalid audio state response")
		}
		ack, err := protocol.DecodeVoiceControlAck(response.Payload)
		if err != nil {
			return err
		}
		if ack.Status == protocol.VoiceControlOK && (ack.Muted != muted || ack.Deafened != deafened) {
			return errors.New("invalid audio state acknowledgement")
		}
		if _, err := state.acceptVoiceCommandAck(generation, expected, ack); err != nil {
			return err
		}
		uncertain = false
		if ack.Status == protocol.VoiceControlStale {
			continue
		}
		return nil
	}
	return ErrVoiceControlStale
}
