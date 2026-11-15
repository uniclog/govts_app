package clientapp

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"uniclog.io/sonoryx/internal/appversion"
	"uniclog.io/sonoryx/internal/audio"
	"uniclog.io/sonoryx/internal/auth"
	voiceclient "uniclog.io/sonoryx/internal/client"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/identity"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

const (
	handshakeAttemptTimeout  = time.Second
	handshakeAttempts        = 3
	audioDeviceChangeTimeout = 10 * time.Second
)

var (
	ErrAlreadyConnected = errors.New("a connection is already active")
	ErrNotConnected     = errors.New("client is not connected")
	ErrClosed           = errors.New("client application is closed")
)

type AudioDeviceSelection struct {
	CaptureID  string
	PlaybackID string
}

type audioDeviceChange struct {
	selection AudioDeviceSelection
	done      chan error
}

type Options struct {
	Logger       *log.Logger
	IdentityPath string
	PinsPath     string
}

type ConnectOptions struct {
	Name             string
	Server           string
	Password         string
	MinServerVersion string
}

type App struct {
	fileOnce            sync.Once
	fileQueue           *FileQueue
	fileTransportMu     sync.Mutex
	fileTransport       *http.Transport
	fileTransportKey    string
	fileRequestSlots    chan struct{}
	lifetimeCtx         context.Context
	cancelLifetime      context.CancelFunc
	state               *voiceclient.State
	events              *EventLog
	logger              *log.Logger
	identityPath        string
	pinsPath            string
	listAudioDevices    func() (audio.DeviceList, error)
	audioDeviceChangeMu sync.Mutex
	audioDeviceChanges  chan audioDeviceChange
	captureDeviceID     string
	playbackDeviceID    string

	// previewMu is taken before mu; see preview.go.
	previewMu          sync.Mutex
	previewWanted      bool
	preview            *micPreview
	monitor            *audio.SwitchablePlayer
	soundPreviewMu     sync.Mutex
	soundPreviewCancel context.CancelFunc
	soundPreviewRun    sync.Mutex
	// presenceMu orders Away and Back, so Back sees what Away turned off.
	presenceMu sync.Mutex
	// awayNow is the presence last requested. The participant flag arrives
	// later, with the server event, so a second request cannot trust it.
	awayNow     bool
	awayTracked bool

	mu                 sync.Mutex
	closed             bool
	active             bool
	runCancel          context.CancelFunc
	runDone            chan struct{}
	connectionPassword string // In-memory credential for automatic reconnection; guarded by mu.
	currentConn        *udp.ClientPacketConn
	chatSession        *chatSession
	serverEndpoint     netip.AddrPort
	chatServerIdentity string
	chatClientIdentity string
	lastError          string
	pendingServerKey   *pendingServerKey
}

func New(options Options) *App {
	ctx, cancel := context.WithCancel(context.Background())
	state := voiceclient.NewState(0, "")
	state.SetConnectionStatus(voiceclient.ConnectionDisconnected)
	if options.Logger == nil {
		options.Logger = log.New(io.Discard, "", 0)
	}
	events := newEventLog()
	events.logger = options.Logger
	return &App{
		lifetimeCtx:        ctx,
		cancelLifetime:     cancel,
		state:              state,
		events:             events,
		logger:             options.Logger,
		identityPath:       options.IdentityPath,
		pinsPath:           options.PinsPath,
		listAudioDevices:   audio.ListDevices,
		audioDeviceChanges: make(chan audioDeviceChange),
	}
}

func (a *App) Connect(options ConnectOptions) error {
	if err := auth.ValidatePassword(options.Password); err != nil {
		return err
	}
	if options.Name == "" {
		return errors.New("display name is required")
	}
	if options.MinServerVersion == "" {
		options.MinServerVersion = appversion.MinimumServerVersion
	}
	if _, err := appversion.Parse(options.MinServerVersion); err != nil {
		return fmt.Errorf("invalid minimum server version: %w", err)
	}
	endpoint, err := ParseServerEndpoint(options.Server)
	if err != nil {
		return err
	}

	// The session owns the microphone pipeline and its meter; release the
	// preview first and resume it if the connection does not start. The
	// monitor blocks transmission, so a new session never starts with it.
	a.previewMu.Lock()
	defer a.previewMu.Unlock()
	a.stopPreviewLocked()
	a.stopMonitorLocked()
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return ErrClosed
	}
	if a.active {
		a.mu.Unlock()
		return ErrAlreadyConnected
	}
	if err := a.state.PrepareConnection(); err != nil {
		a.mu.Unlock()
		a.syncPreviewLocked()
		return err
	}
	defer a.mu.Unlock()
	runCtx, cancel := context.WithCancel(a.lifetimeCtx)
	done := make(chan struct{})
	a.active = true
	a.runCancel = cancel
	a.runDone = done
	a.lastError = ""
	a.serverEndpoint = endpoint
	a.connectionPassword = options.Password
	a.events.clear()
	a.events.append("connection", "Подключение к "+endpoint.String(), 0)
	go a.run(runCtx, done, endpoint, options)
	return nil
}

func (a *App) run(ctx context.Context, done chan struct{}, endpoint netip.AddrPort, options ConnectOptions) {
	err := a.runConnection(ctx, endpoint, options.Name, options.MinServerVersion, options.Password)

	a.mu.Lock()
	a.currentConn = nil
	a.connectionPassword = ""
	a.active = false
	a.runCancel = nil
	if err != nil && !errors.Is(err, context.Canceled) {
		a.lastError = err.Error()
	}
	a.mu.Unlock()
	a.state.SetConnectionStatus(voiceclient.ConnectionDisconnected)
	a.state.Audio.ReleaseAway()
	a.clearAway()
	if err != nil && !errors.Is(err, context.Canceled) {
		a.events.append("error", err.Error(), 0)
	} else {
		a.events.append("connection", "Отключено", 0)
	}
	a.syncPreview()
	close(done)
}

func (a *App) runConnection(ctx context.Context, endpoint netip.AddrPort, name, minServerVersion string, passwords ...string) (runErr error) {
	if len(passwords) > 0 {
		a.mu.Lock()
		a.connectionPassword = passwords[0]
		a.mu.Unlock()
	}
	private, err := a.loadClientIdentity()
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.chatClientIdentity = fmt.Sprintf("%x", private.Public())
	a.mu.Unlock()
	conn, codec, err := openClientPacketConn(endpoint)
	if err != nil {
		return err
	}
	defer func() {
		if err := conn.Close(); err != nil && !shouldReplaceClientSocket(err) {
			runErr = errors.Join(runErr, fmt.Errorf("close UDP connection: %w", err))
		}
	}()

	backoff := newReconnectBackoff()
	everConnected := false
	// The channel the client was in when the last session ended; a reconnect
	// tries to return there instead of the default channel.
	var previousChannel domain.ChannelID
	attempt := 0
	failedHandshakes := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		if !everConnected {
			a.state.SetConnectionStatus(voiceclient.ConnectionConnecting)
		} else {
			a.state.SetConnectionStatus(voiceclient.ConnectionReconnecting)
			// The new session starts present, so Away no longer holds anything.
			a.state.Audio.ReleaseAway()
			a.clearAway()
		}
		attempt++
		a.logger.Printf("handshake starting: attempt=%d server=%s local=%s", attempt, endpoint, conn.LocalAddr())
		a.mu.Lock()
		password := a.connectionPassword
		a.mu.Unlock()
		result, err := voiceclient.PerformSecureHandshakeAttempts(ctx, conn, codec, name, private, a.pinsPath, endpoint.String(), handshakeAttemptTimeout, handshakeAttempts, minServerVersion, password)
		sessionID, joinLevel, permissions := result.SessionID, result.JoinLevel, result.Permissions
		serverIdentity := result.ServerIdentity
		if err != nil {
			a.logger.Printf("handshake failed: attempt=%d server=%s local=%s error=%v", attempt, endpoint, conn.LocalAddr(), err)
			if ctx.Err() != nil {
				return nil
			}
			var keyChange *identity.ServerKeyChangedError
			if errors.As(err, &keyChange) {
				if !a.waitForServerKeyTrust(ctx, *keyChange) {
					return nil
				}
				// Confirmation may outlive the challenge; start a fresh signed
				// handshake and check that the server still has the approved key.
				continue
			}
			var versionError *voiceclient.ServerVersionTooOldError
			if errors.As(err, &versionError) {
				return err
			}
			var rejected *voiceclient.AuthenticationRejectedError
			isRejected := errors.As(err, &rejected)
			// The first connection reports a refusal at once; only a session
			// that already worked waits for the server to accept it again.
			if isRejected && !(everConnected && rejected.Temporary()) {
				return err
			}
			failedHandshakes++
			if shouldReplaceClientSocket(err) || (!isRejected && failedHandshakes%handshakesBeforeSocketReplacement == 0) {
				replacement, replacementCodec, replaceErr := openClientPacketConn(endpoint)
				if replaceErr == nil {
					a.logger.Printf("replacing UDP socket: old_local=%s new_local=%s", conn.LocalAddr(), replacement.LocalAddr())
					_ = conn.Close()
					conn = replacement
					codec = replacementCodec
				} else {
					a.logger.Printf("replace UDP socket failed: error=%v", replaceErr)
				}
			}
			delay := reconnectDelay(backoff.Next())
			a.events.append("reconnect", fmt.Sprintf("Сервер недоступен; повтор через %s", delay), 0)
			if !a.waitForReconnect(ctx, delay) {
				return nil
			}
			continue
		}
		failedHandshakes = 0
		if err := conn.BindSession(sessionID); err != nil {
			return fmt.Errorf("bind UDP session: %w", err)
		}
		a.logger.Printf("handshake succeeded: attempt=%d session_id=%d server=%s local=%s", attempt, sessionID, endpoint, conn.LocalAddr())
		if _, err := a.state.StartSession(sessionID); err != nil {
			return fmt.Errorf("start client session: %w", err)
		}
		a.state.SetAccountPrivileges(joinLevel, permissions)
		chatCtx, cancelChat := context.WithCancel(ctx)
		chat := &chatSession{ctx: chatCtx, cancel: cancelChat}
		a.mu.Lock()
		a.currentConn = conn
		a.chatSession = chat
		a.chatServerIdentity = serverIdentity
		a.mu.Unlock()
		devices := a.AudioDeviceSelection()
		sessionStarted := time.Now()
		sessionErr := runSession(ctx, conn, a.state, devices, a.audioDeviceChanges, name, eventWriter{log: a.events}, a.logger, !everConnected, previousChannel, func() {
			backoff.Reset()
			if everConnected {
				a.events.append("connection", "Соединение восстановлено", uint64(a.state.SnapshotView().Revision))
			} else {
				a.events.append("connection", "Подключено", uint64(a.state.SnapshotView().Revision))
			}
			everConnected = true
		}, cancelChat)
		a.logger.Printf("session ended: session_id=%d server=%s local=%s duration=%s error=%v", sessionID, endpoint, conn.LocalAddr(), time.Since(sessionStarted), sessionErr)
		a.mu.Lock()
		a.currentConn = nil
		a.chatSession = nil
		chat.cancel()
		a.mu.Unlock()
		// Drain chat requests before rebinding the socket or starting a new session.
		chat.requests.Wait()
		if ctx.Err() != nil {
			return nil
		}
		if channelID := a.state.ChannelID(); channelID != 0 {
			previousChannel = channelID
		}
		a.state.InvalidateSession(voiceclient.ConnectionReconnecting)
		if err := conn.ClearSession(sessionID); err != nil {
			return fmt.Errorf("clear UDP session: %w", err)
		}
		codec.Remove(sessionID)
		if !errors.Is(sessionErr, voiceclient.ErrConnectionLost) {
			return sessionErr
		}
		delay := reconnectDelay(backoff.Next())
		a.events.append("reconnect", fmt.Sprintf("Соединение потеряно (%s); повтор через %s", sessionErr, delay), 0)
		if !a.waitForReconnect(ctx, delay) {
			return nil
		}
	}
}

func (a *App) AudioDeviceSelection() AudioDeviceSelection {
	a.mu.Lock()
	defer a.mu.Unlock()
	return AudioDeviceSelection{CaptureID: a.captureDeviceID, PlaybackID: a.playbackDeviceID}
}

func (a *App) waitForReconnect(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (a *App) Disconnect(ctx context.Context) error {
	a.mu.Lock()
	cancel := a.runCancel
	done := a.runDone
	a.mu.Unlock()
	if cancel == nil || done == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *App) Close(ctx context.Context) error {
	a.mu.Lock()
	if !a.closed {
		a.closed = true
		a.cancelLifetime()
	}
	a.mu.Unlock()
	a.stopMonitor()
	err := errors.Join(a.Disconnect(ctx), a.Files().Wait(ctx))
	a.fileTransportMu.Lock()
	if a.fileTransport != nil {
		a.fileTransport.CloseIdleConnections()
	}
	a.fileTransportMu.Unlock()
	return err
}

func (a *App) Wait(ctx context.Context) error {
	a.mu.Lock()
	done := a.runDone
	a.mu.Unlock()
	if done == nil {
		return nil
	}
	select {
	case <-done:
		a.mu.Lock()
		errText := a.lastError
		a.mu.Unlock()
		if errText != "" {
			return errors.New(errText)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *App) JoinChannel(ctx context.Context, channelID domain.ChannelID) error {
	if channelID == 0 {
		return errors.New("channel ID must not be zero")
	}
	a.mu.Lock()
	conn := a.currentConn
	a.mu.Unlock()
	if conn == nil || a.state.ConnectionStatus() != voiceclient.ConnectionConnected {
		return ErrNotConnected
	}
	if err := voiceclient.JoinChannel(ctx, conn, a.state, channelID); err != nil {
		channelName := fmt.Sprintf("%d", channelID)
		for _, channel := range a.state.Snapshot().Channels {
			if channel.ID == channelID {
				channelName = channel.Name
				break
			}
		}
		joinErr := channelJoinError(channelName, err)
		a.events.append("error", joinErr.Error(), 0)
		return joinErr
	}
	return nil
}

func channelJoinError(channelName string, err error) error {
	if strings.Contains(err.Error(), "channel join level is too low") {
		return fmt.Errorf("нет доступа к каналу %q", channelName)
	}
	return fmt.Errorf("не удалось войти в канал %q: %w", channelName, err)
}

func (a *App) Moderate(ctx context.Context, action uint8, targetID uint64, channelID domain.ChannelID) error {
	a.mu.Lock()
	conn := a.currentConn
	a.mu.Unlock()
	if conn == nil || a.state.ConnectionStatus() != voiceclient.ConnectionConnected {
		return ErrNotConnected
	}
	return voiceclient.Moderate(ctx, conn, a.state, action, targetID, channelID)
}

func (a *App) Administer(ctx context.Context, request protocol.AdminRequest) (protocol.AdminResponse, error) {
	a.mu.Lock()
	conn := a.currentConn
	a.mu.Unlock()
	if conn == nil || a.state.ConnectionStatus() != voiceclient.ConnectionConnected {
		return protocol.AdminResponse{}, ErrNotConnected
	}
	response, err := voiceclient.Administer(ctx, conn, a.state, request)
	if err == nil && request.Operation == protocol.AdminSetServerPassword {
		a.mu.Lock()
		if a.currentConn == conn {
			a.connectionPassword = request.Password
		}
		a.mu.Unlock()
	}
	return response, err
}

func (a *App) Snapshot() voiceclient.ClientViewState { return a.state.SnapshotView() }

func (a *App) ConnectionStats() voiceclient.ConnectionStats { return a.state.ConnectionStats() }

func (a *App) Subscribe(ctx context.Context) (<-chan struct{}, func()) {
	return a.state.Subscribe(ctx)
}

func (a *App) EventsAfter(sequence uint64) []ClientEvent { return a.events.after(sequence) }

func (a *App) SubscribeEvents(ctx context.Context) (<-chan struct{}, func()) {
	return a.events.subscribe(ctx)
}

func (a *App) LastError() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastError
}

func ParseServerEndpoint(server string) (netip.AddrPort, error) {
	if ip, err := netip.ParseAddr(server); err == nil {
		return netip.AddrPortFrom(ip, 9000), nil
	}
	endpoint, err := netip.ParseAddrPort(server)
	if err != nil || endpoint.Port() == 0 {
		return netip.AddrPort{}, fmt.Errorf("invalid server address %q: use IP or IP:port with port 1..65535 (IPv6: [IP]:port)", server)
	}
	return endpoint, nil
}

// loadClientIdentity loads or creates the client key and resolves the server
// pin store. Unset paths default to the Sonoryx user config directory.
func (a *App) loadClientIdentity() (ed25519.PrivateKey, error) {
	seedPath := a.identityPath
	if seedPath == "" || a.pinsPath == "" {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		if seedPath == "" {
			seedPath = filepath.Join(configDir, "Sonoryx", "client.seed")
		}
		if a.pinsPath == "" {
			a.pinsPath = filepath.Join(configDir, "Sonoryx", "voice-pins.json")
		}
	}
	private, err := identity.LoadOrCreate(seedPath)
	if err != nil {
		return nil, fmt.Errorf("load client identity: %w", err)
	}
	return private, nil
}

func openClientPacketConn(endpoint netip.AddrPort) (*udp.ClientPacketConn, *protocol.SecureDatagramCodec, error) {
	rawConn, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(endpoint))
	if err != nil {
		return nil, nil, fmt.Errorf("connect UDP: %w", err)
	}
	codec := protocol.NewSecureDatagramCodec(false)
	conn, err := udp.NewClientPacketConn(rawConn, codec)
	if err != nil {
		_ = rawConn.Close()
		return nil, nil, err
	}
	return conn, codec, nil
}
