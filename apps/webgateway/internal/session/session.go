package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"uniclog.io/sonoryx/apps/webgateway/internal/webrtcaudio"
	"uniclog.io/sonoryx/internal/clientcore"
	"uniclog.io/sonoryx/internal/domain"
)

var (
	ErrPeerBusy = errors.New("Согласование голоса уже выполняется")
	ErrNoSocket = errors.New("Подключите WebSocket перед запуском голоса")
)

type Session struct {
	Core           *clientcore.App
	bridge         *webrtcaudio.Bridge
	manager        *Manager
	ctx            context.Context
	cancel         context.CancelFunc
	expires        time.Time
	account, token string

	// The manager lock protects tab ownership and the idle timer.
	socket         bool
	idle           *time.Timer
	idleGeneration uint64

	peerMu    sync.Mutex
	peer      *webrtcaudio.Peer
	peerID    uint64
	lastOffer time.Time

	errMu      sync.RWMutex
	voiceError string
}

func (s *Session) Context() context.Context { return s.ctx }
func (s *Session) Expires() time.Time       { return s.expires }
func (s *Session) Logout()                  { s.cancel() }

func (s *Session) scheduleIdle(deadline time.Time) {
	s.idleGeneration++
	generation := s.idleGeneration
	s.idle = time.AfterFunc(time.Until(deadline), func() {
		s.manager.mu.Lock()
		defer s.manager.mu.Unlock()
		if generation == s.idleGeneration && !s.socket {
			s.cancel()
		}
	})
}

func (s *Session) Attach() (func(), error) {
	s.manager.mu.Lock()
	defer s.manager.mu.Unlock()
	if s.ctx.Err() != nil {
		return nil, ErrExpired
	}
	if s.socket {
		return nil, ErrSocketActive
	}
	s.socket = true
	s.idleGeneration++
	s.idle.Stop()
	return func() {
		s.manager.mu.Lock()
		defer s.manager.mu.Unlock()
		s.socket = false
		if s.ctx.Err() == nil {
			s.scheduleIdle(time.Now().Add(s.manager.cfg.ReconnectGrace))
		}
	}, nil
}

func (s *Session) run() {
	ctx := s.ctx
	events, unsubscribe := s.Core.SubscribeEvents(ctx)
	defer unsubscribe()
	views, unwatch := s.Core.Subscribe(ctx)
	defer unwatch()
	var channel domain.ChannelID
	var udpSession uint64
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
		case <-events:
			if s.Core.ServerIdentityChange() != nil {
				s.setError("Ключ сервера Sonoryx изменился. Обратитесь к оператору")
				_ = s.Core.Disconnect(ctx)
			}
		case <-views:
			snapshot := s.Core.Snapshot()
			if snapshot.ChannelID != channel || snapshot.SessionID != udpSession || snapshot.ConnectionStatus != clientcore.ConnectionConnected {
				s.bridge.Flush()
				channel, udpSession = snapshot.ChannelID, snapshot.SessionID
			}
		}
	}
	s.manager.mu.Lock()
	s.idleGeneration++
	s.idle.Stop()
	delete(s.manager.byToken, s.token)
	s.manager.mu.Unlock()
	s.peerMu.Lock()
	s.bridge.SetPeer(0)
	if s.peer != nil {
		s.peer.Close()
		s.peer = nil
	}
	s.peerMu.Unlock()
	_ = s.Core.Close(context.Background())
	s.manager.mu.Lock()
	delete(s.manager.byAccount, s.account)
	s.manager.mu.Unlock()
}

func (s *Session) setError(message string) {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	s.voiceError = message
}

func (s *Session) ConnectionError() string {
	s.errMu.RLock()
	defer s.errMu.RUnlock()
	if s.voiceError != "" {
		return s.voiceError
	}
	last := s.Core.LastError()
	switch {
	case last == "":
		return ""
	case strings.Contains(last, "too many new accounts"):
		return "Сервер Sonoryx временно ограничил создание аккаунтов с адреса шлюза. Повторите позже"
	case strings.Contains(last, "too many authentication"), strings.Contains(last, "too many pending"):
		return "Сервер Sonoryx временно ограничил подключения с адреса шлюза. Повторите позже"
	case strings.Contains(last, "server is full"), strings.Contains(last, "too many sessions"):
		return "Сервер Sonoryx достиг своего лимита подключений. Повторите позже"
	case strings.Contains(last, "password"):
		return "Сервер Sonoryx отклонил пароль шлюза. Обратитесь к оператору"
	default:
		return "Сервер Sonoryx отклонил подключение или связь завершилась"
	}
}

func (s *Session) Join(ctx context.Context, id domain.ChannelID) error {
	s.bridge.Move(true)
	defer s.bridge.Move(false)
	return s.Core.JoinChannel(ctx, id)
}
func (s *Session) Mute(value bool) error {
	// Core also checks its capture epoch for samples already handed to it.
	if value {
		s.bridge.Mute(true)
	}
	if err := s.Core.SetMuted(value); err != nil {
		return err
	}
	if !value {
		s.bridge.Mute(false)
	}
	return nil
}
func (s *Session) Deafen(value bool) error { return s.Core.SetDeafened(value) }

func (s *Session) Offer(ctx context.Context, offer webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	if err := webrtcaudio.ValidateOffer(offer); err != nil {
		return webrtc.SessionDescription{}, err
	}
	if !s.peerMu.TryLock() {
		return webrtc.SessionDescription{}, ErrPeerBusy
	}
	defer s.peerMu.Unlock()
	s.manager.mu.Lock()
	attached := s.socket
	s.manager.mu.Unlock()
	if !attached {
		return webrtc.SessionDescription{}, ErrNoSocket
	}
	if s.ctx.Err() != nil {
		return webrtc.SessionDescription{}, ErrExpired
	}
	if time.Since(s.lastOffer) < time.Second {
		return webrtc.SessionDescription{}, ErrPeerBusy
	}
	s.lastOffer = time.Now()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()

	// Close the old peer before starting a replacement. Its media can never
	// enter the new bridge generation or open another Sonoryx session.
	s.bridge.SetPeer(0)
	if s.peer != nil {
		s.peer.Close()
		s.peer = nil
	}
	s.peerID++
	cfg := s.manager.cfg
	peer, err := webrtcaudio.NewPeer(cfg.ICE, cfg.ICEPortMin, cfg.ICEPortMax, s.bridge, s.peerID, &s.manager.Audio)
	if err != nil {
		s.manager.Audio.Failures.Add(1)
		return webrtc.SessionDescription{}, err
	}
	answer, err := peer.Answer(ctx, offer)
	if err != nil || ctx.Err() != nil {
		peer.Close()
		s.manager.Audio.Failures.Add(1)
		if err == nil {
			err = ctx.Err()
		}
		return webrtc.SessionDescription{}, err
	}
	s.peer = peer
	s.bridge.SetPeer(s.peerID)
	return answer, nil
}
