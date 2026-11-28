package client

import (
	"context"
	"errors"
	"time"

	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

// Administer sends one remote administration request and returns the decoded
// answer.
func Administer(ctx context.Context, conn *udp.ClientPacketConn, state *State, request protocol.AdminRequest) (protocol.AdminResponse, error) {
	payload, err := protocol.EncodeAdminRequest(request)
	if err != nil {
		return protocol.AdminResponse{}, err
	}
	response, err := DoRequest(ctx, conn, state, protocol.VoicePacket{Type: protocol.PacketAdminRequest, Payload: payload}, 3*time.Second)
	if err != nil {
		return protocol.AdminResponse{}, err
	}
	if response.Type != protocol.PacketAdminAck {
		return protocol.AdminResponse{}, errors.New("unexpected admin response")
	}
	answer, err := protocol.DecodeAdminResponse(response.Payload)
	if err != nil {
		return protocol.AdminResponse{}, err
	}
	if answer.Operation != request.Operation {
		return protocol.AdminResponse{}, errors.New("admin response does not match the request")
	}
	return answer, nil
}
