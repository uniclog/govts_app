package client

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"net"
	"time"

	"uniclog.io/sonoryx/internal/appversion"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

const (
	joinRequestTimeout = 3 * time.Second
	requestAttempts    = 3
)

// errNoReply marks a request the server did not answer: every attempt timed
// out or the datagram could not be sent. The session may still be alive.
var errNoReply = errors.New("no server reply")

type ServerRequestError struct{ Message string }

func (e *ServerRequestError) Error() string { return "server error: " + e.Message }

type ServerVersionTooOldError struct {
	Server   appversion.Number
	Required appversion.Number
}

func (e *ServerVersionTooOldError) Error() string {
	return fmt.Sprintf("Не удалось подключиться: сервер версии %s. Для этой сборки клиента нужен сервер версии %s или новее.", e.Server, e.Required)
}

func newHandshakeRequestID() (uint32, error) {
	for {
		var data [4]byte
		if _, err := rand.Read(data[:]); err != nil {
			return 0, err
		}

		requestID := binary.BigEndian.Uint32(data[:])
		if requestID != 0 {
			return requestID, nil
		}
	}
}

func DoRequest(
	ctx context.Context,
	conn *udp.ClientPacketConn,
	state *State,
	packet protocol.VoicePacket,
	timeout time.Duration,
) (ControlResponse, error) {
	return doRequestAttempts(ctx, conn, state, packet, timeout, requestAttempts)
}

func doRequestAttempts(
	ctx context.Context,
	conn *udp.ClientPacketConn,
	state *State,
	packet protocol.VoicePacket,
	timeout time.Duration,
	attempts int,
	guards ...func() error,
) (ControlResponse, error) {
	if attempts <= 0 {
		return ControlResponse{}, errors.New("request attempts must be positive")
	}
	requestID := state.NextRequestID()
	packet.SessionID = state.SessionID()
	packet.RequestID = requestID

	responseCh := state.RegisterRequest(requestID)
	defer state.CancelRequest(requestID)

	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return ControlResponse{}, err
		}
		for _, guard := range guards {
			if err := guard(); err != nil {
				return ControlResponse{}, err
			}
		}
		if err := conn.SendPacket(packet); err != nil {
			log.Printf("control request send failed: type=%d session_id=%d request_id=%d attempt=%d error=%v", packet.Type, packet.SessionID, requestID, attempt, err)
			if errors.Is(err, net.ErrClosed) {
				return ControlResponse{}, fmt.Errorf("send request %d: %w", requestID, err)
			}
			return ControlResponse{}, fmt.Errorf("send request %d: %w: %w", requestID, errNoReply, err)
		}

		timer := time.NewTimer(timeout)
		select {
		case response := <-responseCh:
			timer.Stop()
			if response.Type == protocol.PacketError {
				log.Printf("control request rejected: type=%d session_id=%d request_id=%d attempt=%d reason=%q", packet.Type, packet.SessionID, requestID, attempt, response.Payload)
				return ControlResponse{}, &ServerRequestError{Message: string(response.Payload)}
			}
			return response, nil
		case <-timer.C:
			log.Printf("control request timeout: type=%d session_id=%d request_id=%d attempt=%d attempts=%d timeout=%s", packet.Type, packet.SessionID, requestID, attempt, attempts, timeout)
			if attempt == attempts {
				if attempts == 1 {
					return ControlResponse{}, fmt.Errorf("request %d timed out: %w", requestID, errNoReply)
				}
				return ControlResponse{}, fmt.Errorf(
					"request %d timed out after %d attempts: %w",
					requestID,
					attempts,
					errNoReply,
				)
			}
		case <-ctx.Done():
			timer.Stop()
			return ControlResponse{}, ctx.Err()
		}
	}
	return ControlResponse{}, fmt.Errorf("request %d failed", requestID)
}
