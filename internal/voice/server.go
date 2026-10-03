package voice

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"time"

	"uniclog.io/govts/internal/logging"
	"uniclog.io/govts/internal/protocol"
	"uniclog.io/govts/internal/transport/udp"
)

func HandlePacket(
	conn *udp.ServerPacketConn,
	hub *Hub,
	cache *RequestCache,
	packet protocol.VoicePacket,
	addr *net.UDPAddr,
) error {
	switch packet.Type {
	case protocol.PacketVoice:
		err := HandleVoicePacket(conn, hub, packet, addr)
		if errors.Is(err, ErrSessionNotFound) || errors.Is(err, ErrInvalidSessionAddr) {
			return nil
		}
		return err
	case protocol.PacketHello:
		return HandleHelloPacket(conn, hub, cache, packet, addr)
	case protocol.PacketHeartbeat:
		return HandleHeartbeatPacket(conn, hub, cache, packet, addr)
	case protocol.PacketDisconnect:
		return HandleDisconnectPacket(hub, cache, packet, addr)
	case protocol.PacketJoinChannel:
		return HandleJoinChannelPacket(conn, hub, cache, packet, addr)
	case protocol.PacketStateSnapshotRequest:
		return HandleStateSnapshotPacket(conn, hub, cache, packet, addr)
	case protocol.PacketMediaCredentialRequest:
		return HandleMediaCredentialPacket(conn, hub, cache, packet, addr)
	case protocol.PacketAudioState:
		return HandleAudioStatePacket(conn, hub, cache, packet, addr)
	case protocol.PacketServerStatus:
		return HandleServerStatusPacket(conn, hub, packet, addr)
	default:
		return fmt.Errorf("invalid packet type: %d", packet.Type)
	}
}

func ServeUDP(
	ctx context.Context,
	conn *udp.ServerPacketConn,
	hub *Hub,
	cache *RequestCache,
	authenticators ...*Authenticator,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	stopWakeup := context.AfterFunc(ctx, func() {
		_ = conn.SetReadDeadline(time.Now())
	})
	defer stopWakeup()
	rejected := logging.NewFailures("server_udp_decode")
	defer rejected.Close()
	var chat *chatWorker
	if len(authenticators) > 0 && authenticators[0] != nil {
		chat = newChatWorker(authenticators[0], conn, hub)
		chatCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { defer close(done); chat.run(chatCtx) }()
		defer func() { cancel(); <-done }()
	}

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

		if packet.Type == protocol.PacketChatRequest && chat != nil {
			if err := chat.enqueue(packet, addr); err != nil {
				log.Printf("chat request rejected: session_id=%d request_id=%d error=%v", packet.SessionID, packet.RequestID, err)
			}
			continue
		}
		var handleErr error
		if len(authenticators) > 0 && authenticators[0] != nil && authenticators[0].policyGate != nil {
			authenticators[0].policyGate.Lock()
		}
		if len(authenticators) > 0 && authenticators[0] != nil && packet.Type == protocol.PacketHello {
			handleErr = conn.WritePacket(0, addr, protocol.NewErrorPacket(0, packet.RequestID, "client upgrade required: secure authentication"))
		} else if len(authenticators) > 0 && authenticators[0] != nil && (packet.Type == protocol.PacketAuthInit || packet.Type == protocol.PacketAuthFinish) {
			handleErr = authenticators[0].Handle(conn, hub, cache, packet, addr)
		} else if len(authenticators) > 0 && authenticators[0] != nil && (packet.Type == protocol.PacketKick || packet.Type == protocol.PacketBan || packet.Type == protocol.PacketDrag) {
			handleErr = authenticators[0].HandleModeration(conn, hub, cache, packet, addr)
		} else {
			handleErr = HandlePacket(conn, hub, cache, packet, addr)
		}
		if len(authenticators) > 0 && authenticators[0] != nil && authenticators[0].policyGate != nil {
			authenticators[0].policyGate.Unlock()
		}
		if err := handleErr; err != nil {
			log.Printf("cannot handle packet: type=%d session_id=%d request_id=%d addr=%v error=%v", packet.Type, packet.SessionID, packet.RequestID, addr, err)
			continue
		}
	}
}

func isMalformedPacket(err error) bool {
	return errors.Is(err, protocol.ErrRejectedDatagram)
}
