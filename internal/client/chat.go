package client

import (
	"context"
	"errors"
	"time"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func (s *State) ChatChanged() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chatRevision++
	s.notifyLocked()
}

func RequestChat(ctx context.Context, conn *udp.ClientPacketConn, state *State, request protocol.ChatRequest) (domain.ChatPage, error) {
	b, err := protocol.EncodeChatRequest(request)
	if err != nil {
		return domain.ChatPage{}, err
	}
	generation, sessionID := state.SessionIdentity()
	checkSession := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		currentGeneration, currentSession := state.SessionIdentity()
		if sessionID == 0 || currentGeneration != generation || currentSession != sessionID || state.ConnectionStatus() != ConnectionConnected {
			return errors.New("сессия чата изменилась")
		}
		return nil
	}
	response, err := doRequestAttempts(ctx, conn, state, protocol.VoicePacket{Type: protocol.PacketChatRequest, Payload: b}, time.Second, 3, checkSession)
	if err != nil {
		return domain.ChatPage{}, err
	}
	if err := checkSession(); err != nil {
		return domain.ChatPage{}, err
	}
	if response.Type != protocol.PacketChatAck {
		return domain.ChatPage{}, errors.New("неподдерживаемый ответ чата")
	}
	return protocol.DecodeChatPage(response.Payload)
}
