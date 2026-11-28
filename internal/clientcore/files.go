package clientcore

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"uniclog.io/sonoryx/internal/clientcore/internal/fileclient"
	voiceclient "uniclog.io/sonoryx/internal/clientcore/internal/live"
	"uniclog.io/sonoryx/internal/protocol"
)

func (a *App) FileTransfers() *fileclient.Client { return a.fileTransfers }

func (a *App) fileSession(expected string) (fileclient.Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	conn, session := a.currentConn, a.chatSession
	if a.closed || conn == nil || session == nil || session.ctx.Err() != nil || a.state.ConnectionStatus() != voiceclient.ConnectionConnected {
		return fileclient.Session{}, ErrNotConnected
	}
	if expected != a.chatContextLocked()+"|"+strconv.FormatInt(a.state.SnapshotView().UserID, 10) {
		return fileclient.Session{}, errors.New("file server or account changed")
	}
	return fileclient.Session{
		Context: session.ctx, Endpoint: a.serverEndpoint.Addr(), ID: a.state.SessionID(),
		Access: func(ctx context.Context) (protocol.FileAccess, error) {
			if !a.beginSessionRequest(session) {
				return protocol.FileAccess{}, ErrNotConnected
			}
			defer session.requests.Done()
			response, err := voiceclient.DoRequest(ctx, conn, a.state, protocol.VoicePacket{Type: protocol.PacketFileAccessRequest}, time.Second)
			if err != nil {
				return protocol.FileAccess{}, err
			}
			var access protocol.FileAccess
			if response.Type != protocol.PacketFileAccessAck || json.Unmarshal(response.Payload, &access) != nil || access.Port == 0 || access.Fingerprint == "" {
				return protocol.FileAccess{}, errors.New("file transfer is disabled on this server")
			}
			return access, nil
		},
	}, nil
}
