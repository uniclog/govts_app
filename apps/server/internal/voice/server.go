package voice

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"time"

	"uniclog.io/sonoryx/internal/logging"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func HandlePacket(
	conn *udp.ServerPacketConn,
	hub *Hub,
	cache *RequestCache,
	packet protocol.VoicePacket,
	addr *net.UDPAddr,
) error {
	switch packet.Type {
	case protocol.PacketVoice, protocol.PacketVoiceBundle:
		return nil // reserved old wire formats, never forwarded
	case protocol.PacketVoiceBundleV2:
		err := HandleVoicePacket(conn, hub, packet, addr)
		if errors.Is(err, ErrSessionNotFound) || errors.Is(err, ErrInvalidSessionAddr) {
			return nil
		}
		return err
	case protocol.PacketHeartbeat:
		return HandleHeartbeatPacket(conn, hub, cache, packet, addr)
	case protocol.PacketVoiceSummary, protocol.PacketVoiceReport:
		return HandleVoiceTelemetry(hub, packet, addr, time.Now())
	case protocol.PacketVoiceStateRequest, protocol.PacketVoiceStateAck:
		return HandleVoiceStateControl(hub, packet, addr, time.Now())
	case protocol.PacketDisconnect:
		return HandleDisconnectPacket(hub, cache, packet, addr)
	case protocol.PacketJoinChannel:
		return HandleJoinChannelPacket(conn, hub, cache, packet, addr)
	case protocol.PacketStateSnapshotRequest:
		return HandleStateSnapshotPacket(conn, hub, cache, packet, addr)
	case protocol.PacketMediaCredentialRequest:
		return HandleMediaCredentialPacket(conn, hub, cache, packet, addr)
	case protocol.PacketFileAccessRequest:
		return HandleFileAccessPacket(conn, hub, cache, packet, addr)
	case protocol.PacketAudioState:
		return HandleAudioStatePacket(conn, hub, cache, packet, addr)
	case protocol.PacketPresence:
		return HandlePresencePacket(conn, hub, cache, packet, addr)
	case protocol.PacketAttention:
		return HandleAttentionPacket(conn, hub, cache, packet, addr)
	default:
		return fmt.Errorf("invalid packet type: %d", packet.Type)
	}
}

func ServeUDP(
	ctx context.Context,
	conn *udp.ServerPacketConn,
	hub *Hub,
	cache *RequestCache,
	authenticator *Authenticator,
) error {
	if authenticator == nil {
		return errors.New("UDP authenticator is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	stopWakeup := context.AfterFunc(ctx, func() {
		_ = conn.SetReadDeadline(time.Now())
	})
	defer stopWakeup()
	rejected := logging.NewFailures("server_udp_decode")
	defer rejected.Close()
	var statusLimit statusLimiter
	chat := newChatWorker(authenticator, conn, hub)
	chatCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); chat.run(chatCtx) }()
	defer func() { cancel(); <-done }()

	for {
		packet, addr, err := conn.ReadPacket()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return ctxErr
			}
			if errors.Is(err, net.ErrClosed) {
				return fmt.Errorf("read UDP packet: %w", err)
			}
			if isMalformedPacket(err) {
				rejected.RecordKind(protocol.DatagramFailureReason(err), err, addr)
				continue
			}
			return fmt.Errorf("read UDP packet: %w", err)
		}

		if protocol.IsServerStatusPacket(packet.Type) {
			handleServerStatus(conn, hub, &statusLimit, packet, addr)
			continue
		}
		if packet.Type == protocol.PacketChatRequest {
			if err := chat.enqueue(packet, addr); err != nil {
				log.Printf("chat request rejected: session_id=%d request_id=%d error=%v", packet.SessionID, packet.RequestID, err)
			}
			continue
		}
		var handleErr error
		if authenticator.policyGate != nil {
			authenticator.policyGate.Lock()
		}
		switch packet.Type {
		case protocol.PacketHello:
			// Clients that predate secure authentication open with a plain Hello.
			handleErr = conn.WritePacket(0, addr, protocol.NewErrorPacket(0, packet.RequestID, "client upgrade required: secure authentication"))
		case protocol.PacketAuthInit, protocol.PacketAuthFinish:
			handleErr = authenticator.Handle(conn, hub, cache, packet, addr)
		case protocol.PacketKick, protocol.PacketBan, protocol.PacketDrag:
			handleErr = authenticator.HandleModeration(conn, hub, cache, packet, addr)
		case protocol.PacketAdminRequest:
			handleErr = authenticator.HandleAdmin(conn, hub, cache, packet, addr)
		default:
			handleErr = HandlePacket(conn, hub, cache, packet, addr)
		}
		if authenticator.policyGate != nil {
			authenticator.policyGate.Unlock()
		}
		if err := handleErr; err != nil {
			if packet.Type == protocol.PacketVoiceBundleV2 || packet.Type == protocol.PacketVoiceSummary || packet.Type == protocol.PacketVoiceReport {
				rejected.RecordKind("voice_packet", err, addr)
				continue
			}
			log.Printf("cannot handle packet: type=%d session_id=%d request_id=%d addr=%v error=%v", packet.Type, packet.SessionID, packet.RequestID, addr, err)
			continue
		}
	}
}

func isMalformedPacket(err error) bool {
	return errors.Is(err, protocol.ErrRejectedDatagram)
}
