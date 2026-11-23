package client

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"time"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/logging"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

var ErrConnectionLost = errors.New("connection lost")

const (
	HeartbeatInterval    = 5 * time.Second
	HeartbeatACKDeadline = 3 * time.Second
	heartbeatAttempts    = 3
	// A session survives a short network stall: reconnecting replaces the whole
	// audio pipeline, while the server keeps the session for 30 s.
	heartbeatMaxMisses = 2
)

func ControlLoop(
	ctx context.Context,
	state *State,
	controlCh <-chan protocol.VoicePacket,
) error {
	generation := state.Generation()
	invalidEvents := logging.NewFailures("client_state_event_decode")
	defer invalidEvents.Close()
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
			case protocol.PacketVoiceSummary, protocol.PacketVoiceReport:
				if packet.RequestID == 0 && packet.Sequence == 0 {
					if err := state.AcceptVoiceTelemetry(generation, packet, time.Now()); err != nil {
						state.RecordVoiceProtocolDrop(generation)
						invalidEvents.RecordKind("voice_telemetry", err, packet.SessionID)
					}
				}
			case protocol.PacketVoiceState:
				if packet.RequestID != 0 || packet.Sequence != 0 {
					continue
				}
				select {
				case state.voicePages <- voiceStateEnvelope{generation: generation, packet: packet}:
				case <-ctx.Done():
					return ctx.Err()
				default:
					// Voice assembly/configuration must not stall heartbeat and
					// snapshot responses. A missing page is recovered by retry.
					state.RequestVoiceResync(generation)
				}
			case protocol.PacketChatChanged:
				if packet.RequestID == 0 && len(packet.Payload) == 0 {
					state.ChatChanged()
				}
			case protocol.PacketAttention:
				if packet.RequestID == 0 && len(packet.Payload) == 0 {
					state.PlayNotification(NotificationWake)
				}
			case protocol.PacketNotice:
				if packet.RequestID != 0 {
					continue
				}
				if notice, err := protocol.DecodeNotice(packet.Payload); err == nil {
					state.Notice(notice)
				} else {
					invalidEvents.RecordKind("notice", err, packet.SessionID)
				}
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
					invalidEvents.Record(err, packet.SessionID)
					state.RequestResync(generation)
					continue
				}
				state.ApplyEvent(generation, event)
			case protocol.PacketJoinChannelAck, protocol.PacketStateSnapshotAck, protocol.PacketModerationAck,
				protocol.PacketHeartbeatAck, protocol.PacketSessionInvalid, protocol.PacketMediaCredentialAck,
				protocol.PacketAudioStateAck, protocol.PacketChatAck, protocol.PacketFileAccessAck,
				protocol.PacketPresenceAck, protocol.PacketAttentionAck, protocol.PacketAdminAck, protocol.PacketNoticePrefsAck:
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
	misses := 0
	probe := func() error {
		err := heartbeatProbe(ctx, conn, state, deadline)
		if err == nil {
			misses = 0
			return nil
		}
		// Any answer from the server, such as SessionInvalid, is final.
		if !errors.Is(err, errNoReply) {
			return err
		}
		misses++
		if misses >= heartbeatMaxMisses {
			return err
		}
		log.Printf("heartbeat missed: misses=%d limit=%d error=%v", misses, heartbeatMaxMisses, err)
		return nil
	}
	// Make the first RTT available as soon as the session starts.
	if err := probe(); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := probe(); err != nil {
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
	generation := state.Generation()
	started := time.Now()
	count, end := state.VoiceSentInWindow()
	// The 30-second range uses 16 bits for the final sequence and 14 for its size.
	window := uint32(end)<<14 | uint32(count)
	attempts := 0
	response, err := doRequestAttempts(ctx, conn, state, protocol.VoicePacket{Type: protocol.PacketHeartbeat, Sequence: window}, attemptTimeout, heartbeatAttempts, func() error { attempts++; return nil })
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
	if attempts == 1 {
		state.RecordVoiceRTT(generation, response.RequestID, time.Since(started), time.Now())
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

// disconnectCopies and disconnectSpacing: Disconnect has no acknowledgement,
// so a deliberate exit sends a few copies; the server ignores the extra ones.
const (
	disconnectCopies  = 3
	disconnectSpacing = 15 * time.Millisecond
)

func Disconnect(conn *udp.ClientPacketConn, sessionID uint64) error {
	packet := protocol.VoicePacket{
		Type:      protocol.PacketDisconnect,
		SessionID: sessionID,
	}
	var err error
	for copy := 0; copy < disconnectCopies; copy++ {
		if copy > 0 {
			time.Sleep(disconnectSpacing)
		}
		if sendErr := conn.SendPacket(packet); sendErr != nil {
			err = sendErr
		}
	}
	return err
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
) (runErr error) {
	generation := state.Generation()
	if channelID == 0 {
		return errors.New("channel ID must not be zero")
	}
	unlock, err := state.lockVoiceCommand(ctx, generation)
	if err != nil {
		return err
	}
	defer unlock()
	changingChannel := state.ChannelID() != channelID
	if err := state.beginVoiceCommand(generation, changingChannel, changingChannel); err != nil {
		return err
	}
	uncertain := false
	defer func() { runErr = state.finishVoiceCommand(generation, uncertain, runErr) }()
	for attempt := 0; attempt < 3; attempt++ {
		expected, err := state.voiceCommandRevision(generation)
		if err != nil {
			return err
		}
		payload, err := protocol.EncodeJoinChannelRequest(channelID, expected)
		if err != nil {
			return err
		}
		response, err := doRequestAttempts(ctx, conn, state, protocol.VoicePacket{Type: protocol.PacketJoinChannel, Payload: payload}, timeout, requestAttempts, func() error {
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
			return fmt.Errorf("join channel %d: %w", channelID, err)
		}
		if response.Type != protocol.PacketJoinChannelAck {
			return fmt.Errorf("unexpected join response: type=%d", response.Type)
		}
		ack, err := protocol.DecodeVoiceControlAck(response.Payload)
		if err != nil {
			return err
		}
		if ack.Status == protocol.VoiceControlOK {
			if err := validateJoinResponse(response, channelID); err != nil {
				return err
			}
		}
		current, err := state.acceptVoiceCommandAck(generation, expected, ack)
		if err != nil {
			return err
		}
		uncertain = false
		if ack.Status == protocol.VoiceControlStale {
			continue
		}
		if current && !state.ConfirmChannel(generation, channelID, ack.DomainRevision) {
			return ErrConnectionLost
		}
		return nil
	}
	return ErrVoiceControlStale
}

func validateJoinResponse(response ControlResponse, requestedChannel domain.ChannelID) error {
	if response.Type != protocol.PacketJoinChannelAck {
		return fmt.Errorf("unexpected join response: type=%d", response.Type)
	}
	ack, err := protocol.DecodeVoiceControlAck(response.Payload)
	if err != nil {
		return fmt.Errorf("invalid join response: %w", err)
	}
	if ack.Status != protocol.VoiceControlOK {
		return ErrVoiceControlStale
	}
	if ack.ChannelID != requestedChannel {
		return fmt.Errorf(
			"join response channel mismatch: got %d, want %d",
			ack.ChannelID,
			requestedChannel,
		)
	}
	return nil
}
