package clientcore

import (
	"context"
	"errors"
	"strconv"
	"sync"

	voiceclient "uniclog.io/sonoryx/internal/clientcore/internal/live"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

type chatSession struct {
	ctx      context.Context
	cancel   context.CancelFunc
	requests sync.WaitGroup
}

func (a *App) ChatRequest(ctx context.Context, expectedContext string, request protocol.ChatRequest) (domain.ChatPage, error) {
	a.mu.Lock()
	conn := a.currentConn
	session := a.chatSession
	currentContext := a.chatContextLocked() + "|" + strconv.FormatInt(a.state.SnapshotView().UserID, 10)
	if conn == nil || session == nil || session.ctx.Err() != nil || a.state.ConnectionStatus() != voiceclient.ConnectionConnected {
		a.mu.Unlock()
		return domain.ChatPage{}, ErrNotConnected
	}
	if expectedContext != currentContext {
		a.mu.Unlock()
		return domain.ChatPage{}, errors.New("сервер или учётная запись чата изменились")
	}
	session.requests.Add(1)
	a.mu.Unlock()
	defer session.requests.Done()
	requestCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(session.ctx, cancel)
	defer stop()
	defer cancel()
	if session.ctx.Err() != nil {
		cancel()
	}
	page, err := voiceclient.RequestChat(requestCtx, conn, a.state, request)
	if err == nil && request.Operation == protocol.ChatSend {
		a.state.PlayNotification(voiceclient.NotificationMessageSent)
	}
	return page, err
}

func (a *App) ChatContext() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.chatContextLocked()
}

func (a *App) chatContextLocked() string {
	return a.serverEndpoint.String() + "|" + a.chatServerIdentity + "|" + a.chatClientIdentity
}

// Keep a control request from racing with socket rebinding after disconnect.
func (a *App) beginSessionRequest(session *chatSession) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.chatSession != session || session.ctx.Err() != nil {
		return false
	}
	session.requests.Add(1)
	return true
}
