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
	MinimumServerVersion    = "0.1.0"
	handshakeRequestTimeout = 3 * time.Second
	joinRequestTimeout      = 3 * time.Second
	requestAttempts         = 3
	maxClientNameBytes      = 64
)

type ServerVersionTooOldError struct {
	Server   appversion.Number
	Required appversion.Number
}

func (e *ServerVersionTooOldError) Error() string {
	return fmt.Sprintf("Не удалось подключиться: сервер версии %s. Для этой сборки клиента нужен сервер версии %s или новее.", e.Server, e.Required)
}

func PerformHandshake(
	ctx context.Context,
	conn *udp.ClientPacketConn,
	name string,
) (uint64, error) {
	if len(name) == 0 {
		return 0, errors.New("client name is required")
	}
	if len([]byte(name)) > maxClientNameBytes {
		return 0, fmt.Errorf("client name too long: %d bytes", len([]byte(name)))
	}

	requestID, err := newHandshakeRequestID()
	if err != nil {
		return 0, fmt.Errorf("create handshake request ID: %w", err)
	}

	return performHandshakeWithRequestID(
		ctx,
		conn,
		name,
		requestID,
		handshakeRequestTimeout,
	)
}

func PerformHandshakeAttempt(
	ctx context.Context,
	conn *udp.ClientPacketConn,
	name string,
	timeout time.Duration,
) (uint64, error) {
	if len(name) == 0 {
		return 0, errors.New("client name is required")
	}
	if len([]byte(name)) > maxClientNameBytes {
		return 0, fmt.Errorf("client name too long: %d bytes", len([]byte(name)))
	}
	requestID, err := newHandshakeRequestID()
	if err != nil {
		return 0, fmt.Errorf("create handshake request ID: %w", err)
	}
	return performHandshake(ctx, conn, name, requestID, timeout, 1)
}

// PerformHandshakeAttempts retries the same idempotent Hello request. Reusing
// its RequestID lets the server request cache return the original session when
// the request arrived but the acknowledgement was lost.
func PerformHandshakeAttempts(
	ctx context.Context,
	conn *udp.ClientPacketConn,
	name string,
	timeout time.Duration,
	attempts int,
	minimumVersion ...string,
) (uint64, error) {
	if len(name) == 0 {
		return 0, errors.New("client name is required")
	}
	if len([]byte(name)) > maxClientNameBytes {
		return 0, fmt.Errorf("client name too long: %d bytes", len([]byte(name)))
	}
	requestID, err := newHandshakeRequestID()
	if err != nil {
		return 0, fmt.Errorf("create handshake request ID: %w", err)
	}
	return performHandshake(ctx, conn, name, requestID, timeout, attempts, minimumVersion...)
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

func performHandshakeWithRequestID(
	ctx context.Context,
	conn *udp.ClientPacketConn,
	name string,
	requestID uint32,
	timeout time.Duration,
) (uint64, error) {
	return performHandshake(ctx, conn, name, requestID, timeout, requestAttempts)
}

func performHandshake(
	ctx context.Context,
	conn *udp.ClientPacketConn,
	name string,
	requestID uint32,
	timeout time.Duration,
	attempts int,
	minimumVersion ...string,
) (uint64, error) {
	if requestID == 0 {
		return 0, errors.New("handshake request ID must not be zero")
	}
	if timeout <= 0 {
		return 0, errors.New("handshake timeout must be positive")
	}
	if attempts <= 0 {
		return 0, errors.New("handshake attempts must be positive")
	}
	required := MinimumServerVersion
	if len(minimumVersion) > 0 && minimumVersion[0] != "" {
		required = minimumVersion[0]
	}
	minimum, err := appversion.Parse(required)
	if err != nil {
		return 0, fmt.Errorf("invalid minimum server version: %w", err)
	}

	hello := protocol.VoicePacket{
		Type:      protocol.PacketHello,
		RequestID: requestID,
		Sequence:  uint32(minimum),
		Payload:   []byte(name),
	}

	defer conn.SetReadDeadline(time.Time{})

	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return 0, err
		}

		deadline := time.Now().Add(timeout)
		if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
			deadline = contextDeadline
		}
		if err := conn.SetReadDeadline(deadline); err != nil {
			return 0, err
		}

		if err := conn.SendPacket(hello); err != nil {
			return 0, fmt.Errorf("send handshake request %d: %w", requestID, err)
		}

		for {
			ack, err := conn.ReceivePacket()
			if err != nil {
				if errors.Is(err, protocol.ErrRejectedDatagram) {
					continue
				}
				var netErr net.Error
				if errors.As(err, &netErr) && netErr.Timeout() {
					if ctxErr := ctx.Err(); ctxErr != nil {
						return 0, ctxErr
					}
					if attempt < attempts {
						break
					}
					if attempts == 1 {
						return 0, fmt.Errorf("handshake request %d timed out", requestID)
					}
					return 0, fmt.Errorf(
						"handshake request %d timed out after %d attempts",
						requestID,
						attempts,
					)
				}
				return 0, err
			}

			if ack.RequestID != requestID {
				continue
			}
			if ack.Type == protocol.PacketError {
				return 0, fmt.Errorf("server error: %s", string(ack.Payload))
			}
			if ack.Type == protocol.PacketServerVersionTooOld {
				return 0, &ServerVersionTooOldError{Server: appversion.Number(ack.Sequence), Required: minimum}
			}
			if ack.Type != protocol.PacketHelloAck {
				continue
			}
			if ack.SessionID == 0 {
				return 0, errors.New("server returned invalid session ID")
			}
			if appversion.Number(ack.Sequence) < minimum {
				return 0, &ServerVersionTooOldError{Server: appversion.Number(ack.Sequence), Required: minimum}
			}

			return ack.SessionID, nil
		}
	}

	return 0, errors.New("handshake failed")
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
			return ControlResponse{}, fmt.Errorf("send request %d: %w", requestID, err)
		}

		timer := time.NewTimer(timeout)
		select {
		case response := <-responseCh:
			timer.Stop()
			if response.Type == protocol.PacketError {
				log.Printf("control request rejected: type=%d session_id=%d request_id=%d attempt=%d reason=%q", packet.Type, packet.SessionID, requestID, attempt, response.Payload)
				return ControlResponse{}, fmt.Errorf("server error: %s", string(response.Payload))
			}
			return response, nil
		case <-timer.C:
			log.Printf("control request timeout: type=%d session_id=%d request_id=%d attempt=%d attempts=%d timeout=%s", packet.Type, packet.SessionID, requestID, attempt, attempts, timeout)
			if attempt == attempts {
				if attempts == 1 {
					return ControlResponse{}, fmt.Errorf("request %d timed out", requestID)
				}
				return ControlResponse{}, fmt.Errorf(
					"request %d timed out after %d attempts",
					requestID,
					attempts,
				)
			}
		case <-ctx.Done():
			timer.Stop()
			return ControlResponse{}, ctx.Err()
		}
	}
	return ControlResponse{}, fmt.Errorf("request %d failed", requestID)
}
