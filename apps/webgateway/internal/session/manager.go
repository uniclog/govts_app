package session

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/text/unicode/norm"
	"golang.org/x/time/rate"
	"uniclog.io/sonoryx/apps/webgateway/internal/config"
	"uniclog.io/sonoryx/apps/webgateway/internal/webrtcaudio"
	"uniclog.io/sonoryx/internal/clientcore"
	"uniclog.io/sonoryx/internal/identity"
)

var (
	ErrCredentials  = errors.New("Неверный аккаунт или код доступа")
	ErrNickname     = errors.New("Введите никнейм длиной до 64 байт")
	ErrDuplicate    = errors.New("Этот аккаунт уже открыт в другой веб-сессии")
	ErrCapacity     = errors.New("Все места веб-шлюза заняты")
	ErrRate         = errors.New("Слишком много подключений. Повторите позже")
	ErrExpired      = errors.New("Веб-сессия завершена. Войдите заново")
	ErrSocketActive = errors.New("Эта веб-сессия уже открыта в другой вкладке")
)

type Manager struct {
	cfg          config.Config
	pinsPath     string
	accounts     map[string]Account
	admission    *rate.Limiter
	registration *rate.Limiter

	mu            sync.Mutex
	closed        bool
	byToken       map[string]*Session
	byAccount     map[string]*Session
	lastLogin     map[string]time.Time
	workers       sync.WaitGroup
	Audio         webrtcaudio.Counters
	LoginFailures uint64
}

func NewManager(cfg config.Config, pinsPath string, accounts map[string]Account) *Manager {
	return &Manager{
		cfg: cfg, pinsPath: filepath.Clean(pinsPath), accounts: accounts, admission: rate.NewLimiter(rate.Every(3*time.Second), 6),
		registration: rate.NewLimiter(rate.Every(6*time.Minute), 5),
		byToken:      make(map[string]*Session), byAccount: make(map[string]*Session), lastLogin: make(map[string]time.Time),
	}
}

func (m *Manager) Login(accountID, credential string) (*Session, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.accounts[accountID]
	if !a.Accepts(credential) {
		m.LoginFailures++
		return nil, "", ErrCredentials
	}
	return m.open(a, false)
}

func (m *Manager) LoginNickname(name, browserToken string) (*Session, string, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	name = norm.NFKC.String(name)
	if !validName(name) {
		m.LoginFailures++
		return nil, "", "", ErrNickname
	}
	account := m.browserAccount(name, browserToken)
	var fresh bool
	if account.ID != "" {
		if _, err := os.Stat(account.identityPath); errors.Is(err, os.ErrNotExist) {
			account = Account{}
		} else if err != nil {
			return nil, "", "", err
		}
	}
	if account.ID == "" {
		fresh = true
		browserToken = NewToken()
		account = m.browserAccount(name, browserToken)
	}
	s, token, err := m.open(account, fresh)
	return s, token, browserToken, err
}

func (m *Manager) browserAccount(name, token string) Account {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(raw) != 32 {
		return Account{}
	}
	id := fmt.Sprintf("browser-%x", sha256.Sum256(raw))
	return Account{ID: id, Name: name, identityPath: filepath.Join(m.cfg.StateDir, "browsers", id+".seed")}
}

// open is called with m.mu held.
func (m *Manager) open(a Account, fresh bool) (*Session, string, error) {
	if m.closed {
		return nil, "", ErrExpired
	}
	if m.byAccount[a.ID] != nil {
		return nil, "", ErrDuplicate
	}
	if len(m.byAccount) >= m.cfg.MaxSessions {
		return nil, "", ErrCapacity
	}
	now := time.Now()
	for id, at := range m.lastLogin {
		if now.Sub(at) >= 5*time.Second {
			delete(m.lastLogin, id)
		}
	}
	if now.Sub(m.lastLogin[a.ID]) < 5*time.Second || !m.admission.Allow() {
		return nil, "", ErrRate
	}
	if fresh {
		if !m.registration.Allow() {
			return nil, "", ErrRate
		}
		if _, err := identity.LoadOrCreate(a.identityPath); err != nil {
			return nil, "", err
		}
	}
	expires := now.Add(m.cfg.SessionTTL)
	ctx, cancel := context.WithDeadline(context.Background(), expires)
	bridge := webrtcaudio.NewBridge()
	core := clientcore.New(clientcore.Options{Audio: bridge, IdentityPath: a.identityPath, PinsPath: m.pinsPath})
	_ = core.SetMuted(true)
	if err := core.Connect(clientcore.ConnectOptions{Server: m.cfg.Endpoint, Name: a.Name, Password: m.cfg.Password}); err != nil {
		cancel()
		_ = core.Close(context.Background())
		return nil, "", err
	}
	token := NewToken()
	s := &Session{Core: core, bridge: bridge, manager: m, ctx: ctx, cancel: cancel, expires: expires, account: a.ID, token: token}
	m.byAccount[a.ID], m.byToken[token] = s, s
	m.lastLogin[a.ID] = now
	s.scheduleIdle(now.Add(m.cfg.ReconnectGrace))
	m.workers.Go(s.run)
	return s, token, nil
}

func (m *Manager) Lookup(token string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.byToken[token]
	if s == nil || s.ctx.Err() != nil {
		return nil
	}
	return s
}

func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	for _, s := range m.byToken {
		s.cancel()
	}
	m.mu.Unlock()
	m.workers.Wait()
}

type Metrics struct {
	Active                     int
	LoginFailures              uint64
	WebRTCFailures             uint64
	RTPReceived                uint64
	RTPMissing                 uint64
	PlaybackQueueMeanMS        float64
	SonoryxRTTMeanMS           float64
	SonoryxIncomingLossMeanPct float64
	SonoryxOutgoingLossMeanPct float64
}

func (m *Manager) Metrics() Metrics {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := Metrics{Active: len(m.byToken), LoginFailures: m.LoginFailures, WebRTCFailures: m.Audio.Failures.Load(), RTPReceived: m.Audio.RTPReceived.Load(), RTPMissing: m.Audio.RTPMissing.Load()}
	if samples := m.Audio.PCMSamples.Load(); samples > 0 {
		result.PlaybackQueueMeanMS = float64(m.Audio.PCMDelayMicros.Load()) / float64(samples) / 1000
	}
	var rtt, in, out int
	for _, s := range m.byToken {
		stats := s.Core.ConnectionStats()
		if stats.PingAvailable {
			rtt++
			result.SonoryxRTTMeanMS += stats.PingMS
		}
		if stats.IncomingKnown {
			in++
			result.SonoryxIncomingLossMeanPct += stats.IncomingLoss
		}
		if stats.OutgoingKnown {
			out++
			result.SonoryxOutgoingLossMeanPct += stats.OutgoingLoss
		}
	}
	result.SonoryxRTTMeanMS /= float64(max(rtt, 1))
	result.SonoryxIncomingLossMeanPct /= float64(max(in, 1))
	result.SonoryxOutgoingLossMeanPct /= float64(max(out, 1))
	return result
}
