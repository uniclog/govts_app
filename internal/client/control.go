package client

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"time"

	"uniclog.io/govts/internal/domain"
	"uniclog.io/govts/internal/protocol"
	"uniclog.io/govts/internal/transport/udp"
)

var ErrConnectionLost = errors.New("connection lost")

const (
	HeartbeatInterval    = 5 * time.Second
	HeartbeatACKDeadline = 3 * time.Second
	heartbeatAttempts    = 3
)

func ControlLoop(
	ctx context.Context,
	state *State,
	controlCh <-chan protocol.VoicePacket,
) error {
	generation := state.Generation()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case packet, ok := <-controlCh:
			if !ok {
				return nil
			}
			if packet.SessionID != state.SessionID() {
				log.Printf("ignoring control packet for session %d", packet.SessionID)
				continue
			}

			switch packet.Type {
			case protocol.PacketAccountPrivileges:
				if packet.RequestID != 0 || len(packet.Payload) != 3 {
					continue
				}
				state.SetAccountPrivileges(binary.BigEndian.Uint16(packet.Payload[:2]), packet.Payload[2])
			case protocol.PacketStateEvent:
				if packet.RequestID != 0 {
					continue
				}
				event, err := protocol.DecodeStateEvent(packet.Payload)
				if err != nil {
					state.RequestResync(generation)
					continue
				}
				state.ApplyEvent(generation, event)
			case protocol.PacketJoinChannelAck, protocol.PacketStateSnapshotAck, protocol.PacketModerationAck,
				protocol.PacketHeartbeatAck, protocol.PacketSessionInvalid, protocol.PacketMediaCredentialAck,
				protocol.PacketAudioStateAck:
				response := ControlResponse{
					Type:      packet.Type,
					RequestID: packet.RequestID,
					Sequence:  packet.Sequence,
					Payload:   packet.Payload,
				}
				if !state.CompleteRequest(response) {
					log.Printf("response for unknown request: request_id=%d", packet.RequestID)
				}
			case protocol.PacketError:
				response := ControlResponse{
					Type:      packet.Type,
					RequestID: packet.RequestID,
					Payload:   packet.Payload,
				}
				if !state.CompleteRequest(response) {
					log.Printf("error response for unknown request: request_id=%d", packet.RequestID)
				}
			default:
				log.Printf("unhandled control packet: type=%d", packet.Type)
			}
		}
	}
}

func RequestMediaCredential(ctx context.Context, conn *udp.ClientPacketConn, state *State) ([32]byte, error) {
	var credential [32]byte
	response, err := DoRequest(ctx, conn, state, protocol.VoicePacket{Type: protocol.PacketMediaCredentialRequest}, joinRequestTimeout)
	if err != nil {
		return credential, err
	}
	if response.Type != protocol.PacketMediaCredentialAck || len(response.Payload) != len(credential) {
		return credential, errors.New("invalid media credential response")
	}
	copy(credential[:], response.Payload)
	return credential, nil
}

func HeartbeatLoop(
	ctx context.Context,
	conn *udp.ClientPacketConn,
	state *State,
) error {
	return heartbeatLoop(ctx, conn, state, HeartbeatInterval, HeartbeatACKDeadline)
}

func heartbeatLoop(ctx context.Context, conn *udp.ClientPacketConn, state *State, interval, deadline time.Duration) error {
	if interval <= 0 || deadline <= 0 {
		return errors.New("heartbeat interval and deadline must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	// Make the first RTT available as soon as the session starts.
	if err := heartbeatProbe(ctx, conn, state, deadline); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := heartbeatProbe(ctx, conn, state, deadline); err != nil {
				return err
			}
		}
	}
}

func heartbeatProbe(ctx context.Context, conn *udp.ClientPacketConn, state *State, deadline time.Duration) error {
	if deadline <= 0 {
		return fmt.Errorf("%w: heartbeat deadline must be positive", ErrConnectionLost)
	}
	attemptTimeout := deadline / heartbeatAttempts
	started := time.Now()
	count, end := state.VoiceSentInWindow()
	// The 30-second range uses 16 bits for the final sequence and 14 for its size.
	window := uint32(end)<<14 | uint32(count)
	response, err := doRequestAttempts(ctx, conn, state, protocol.VoicePacket{Type: protocol.PacketHeartbeat, Sequence: window}, attemptTimeout, heartbeatAttempts)
	if err != nil {
		return fmt.Errorf("%w: heartbeat: %w", ErrConnectionLost, err)
	}
	if response.Type != protocol.PacketHeartbeatAck && response.Type != protocol.PacketSessionInvalid {
		return fmt.Errorf("%w: unexpected heartbeat response type %d", ErrConnectionLost, response.Type)
	}
	if err := protocol.ValidateEmptyLifecyclePayload(response.Type, response.Payload); err != nil {
		return fmt.Errorf("%w: %v", ErrConnectionLost, err)
	}
	if response.Type == protocol.PacketSessionInvalid {
		return fmt.Errorf("%w: server rejected session", ErrConnectionLost)
	}
	state.MarkHeartbeatAck(time.Now())
	state.measurements.recordPing(time.Since(started), time.Now())
	if response.Sequence <= 10000 {
		state.measurements.recordOutgoingLoss(float64(response.Sequence) / 100)
	} else {
		state.measurements.clearOutgoingLoss()
	}
	return nil
}

func Disconnect(conn *udp.ClientPacketConn, sessionID uint64) error {
	packet := protocol.VoicePacket{
		Type:      protocol.PacketDisconnect,
		SessionID: sessionID,
	}
	return conn.SendPacket(packet)
}

func JoinChannel(
	ctx context.Context,
	conn *udp.ClientPacketConn,
	state *State,
	channelID domain.ChannelID,
) error {
	return joinChannelWithTimeout(ctx, conn, state, channelID, joinRequestTimeout)
}

func joinChannelWithTimeout(
	ctx context.Context,
	conn *udp.ClientPacketConn,
	state *State,
	channelID domain.ChannelID,
	timeout time.Duration,
) error {
	generation := state.Generation()
	payload, err := protocol.EncodeJoinChannelRequest(channelID)
	if err != nil {
		return err
	}

	packet := protocol.VoicePacket{
		Type:    protocol.PacketJoinChannel,
		Payload: payload,
	}
	response, err := DoRequest(ctx, conn, state, packet, timeout)
	if err != nil {
		return fmt.Errorf("join channel %d: %w", channelID, err)
	}
	if err := validateJoinResponse(response, channelID); err != nil {
		return err
	}

	_, revision, _ := protocol.DecodeJoinChannelAck(response.Payload)
	if !state.ConfirmChannel(generation, channelID, revision) {
		return errors.New("client session changed during join")
	}
	return nil
}

func validateJoinResponse(response ControlResponse, requestedChannel domain.ChannelID) error {
	if response.Type != protocol.PacketJoinChannelAck {
		return fmt.Errorf("unexpected join response: type=%d", response.Type)
	}
	confirmedChannel, _, err := protocol.DecodeJoinChannelAck(response.Payload)
	if err != nil {
		return fmt.Errorf("invalid join response: %w", err)
	}
	if confirmedChannel != requestedChannel {
		return fmt.Errorf(
			"join response channel mismatch: got %d, want %d",
			confirmedChannel,
			requestedChannel,
		)
	}
	return nil
}
