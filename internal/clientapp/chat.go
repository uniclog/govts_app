package clientapp

import (
	"context"
	"errors"
	"strconv"
	"sync"

	voiceclient "uniclog.io/sonoryx/internal/client"
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
	return voiceclient.RequestChat(requestCtx, conn, a.state, request)
}

func (a *App) ChatContext() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.chatContextLocked()
}

func (a *App) chatContextLocked() string {
	return a.serverEndpoint.String() + "|" + a.chatServerIdentity + "|" + a.chatClientIdentity
}
