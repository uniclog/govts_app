package clientapp

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"uniclog.io/sonoryx/internal/appversion"
	"uniclog.io/sonoryx/internal/audio"
	"uniclog.io/sonoryx/internal/audio/voicegate"
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
	Commands     <-chan voiceclient.Command
	Output       io.Writer
	Logger       *log.Logger
	Secure       bool
	IdentityPath string
	PinsPath     string
}

type ConnectOptions struct {
	Name             string
	Server           string
	MinServerVersion string
}

type App struct {
	lifetimeCtx         context.Context
	cancelLifetime      context.CancelFunc
	state               *voiceclient.State
	events              *EventLog
	commands            <-chan voiceclient.Command
	output              io.Writer
	logger              *log.Logger
	secure              bool
	identityPath        string
	pinsPath            string
	listAudioDevices    func() (audio.DeviceList, error)
	audioDeviceChangeMu sync.Mutex
	audioDeviceChanges  chan audioDeviceChange
	captureDeviceID     string
	playbackDeviceID    string

	// previewMu is taken before mu; see preview.go.
	previewMu      sync.Mutex
	previewWanted  bool
	previewMonitor bool
	preview        *micPreview

	mu                 sync.Mutex
	closed             bool
	active             bool
	runCancel          context.CancelFunc
	runDone            chan struct{}
	currentConn        *udp.ClientPacketConn
	chatSession        *chatSession
	serverEndpoint     netip.AddrPort
	chatServerIdentity string
	chatClientIdentity string
	lastError          string
}

func New(options Options) *App {
	ctx, cancel := context.WithCancel(context.Background())
	state := voiceclient.NewState(0, "")
	state.SetConnectionStatus(voiceclient.ConnectionDisconnected)
	if options.Output == nil {
		options.Output = io.Discard
	}
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
		commands:           options.Commands,
		output:             options.Output,
		logger:             options.Logger,
		secure:             options.Secure,
		identityPath:       options.IdentityPath,
		pinsPath:           options.PinsPath,
		listAudioDevices:   audio.ListDevices,
		audioDeviceChanges: make(chan audioDeviceChange),
	}
}

func (a *App) Connect(options ConnectOptions) error {
	if options.Name == "" {
		return errors.New("display name is required")
	}
	if options.MinServerVersion == "" {
		if a.secure {
			options.MinServerVersion = appversion.MinimumServerVersion
		} else {
			options.MinServerVersion = voiceclient.MinimumServerVersion
		}
	}
	if _, err := appversion.Parse(options.MinServerVersion); err != nil {
		return fmt.Errorf("invalid minimum server version: %w", err)
	}
	endpoint, err := ParseServerEndpoint(options.Server)
	if err != nil {
		return err
	}

	// The session owns the microphone pipeline and its meter; release the
	// preview first and resume it if the connection does not start.
	a.previewMu.Lock()
	defer a.previewMu.Unlock()
	a.stopPreviewLocked()
	a.previewMonitor = false
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
	a.events.clear()
	a.events.append("connection", "Подключение к "+endpoint.String(), 0)
	go a.run(runCtx, done, endpoint, options)
	return nil
}

func (a *App) run(ctx context.Context, done chan struct{}, endpoint netip.AddrPort, options ConnectOptions) {
	err := a.runConnection(ctx, endpoint, options.Name, options.MinServerVersion)

	a.mu.Lock()
	a.currentConn = nil
	a.active = false
	a.runCancel = nil
	if err != nil && !errors.Is(err, context.Canceled) {
		a.lastError = err.Error()
	}
	a.mu.Unlock()
	a.state.SetConnectionStatus(voiceclient.ConnectionDisconnected)
	if err != nil && !errors.Is(err, context.Canceled) {
		a.events.append("error", err.Error(), 0)
	} else {
		a.events.append("connection", "Отключено", 0)
	}
	a.syncPreview()
	close(done)
}

func (a *App) runConnection(ctx context.Context, endpoint netip.AddrPort, name, minServerVersion string) (runErr error) {
	var private ed25519.PrivateKey
	if a.secure {
		configDir, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		seedPath := a.identityPath
		if seedPath == "" {
			seedPath = filepath.Join(configDir, "Govts", "client.seed")
		}
		private, err = identity.LoadOrCreate(seedPath)
		if err != nil {
			return fmt.Errorf("load client identity: %w", err)
		}
		a.mu.Lock()
		a.chatClientIdentity = fmt.Sprintf("%x", private.Public())
		a.mu.Unlock()
		if a.pinsPath == "" {
			a.pinsPath = filepath.Join(configDir, "Govts", "voice-pins.json")
		}
	}
	open := func() (*udp.ClientPacketConn, *protocol.SecureDatagramCodec, error) {
		if a.secure {
			return openSecureClientPacketConn(endpoint)
		}
		conn, err := openClientPacketConn(endpoint)
		return conn, nil, err
	}
	conn, secureCodec, err := open()
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
	attempt := 0
	for {
		if ctx.Err() != nil {
			return nil
		}
		if !everConnected {
			a.state.SetConnectionStatus(voiceclient.ConnectionConnecting)
		} else {
			a.state.SetConnectionStatus(voiceclient.ConnectionReconnecting)
		}
		var sessionID uint64
		attempt++
		a.logger.Printf("handshake starting: attempt=%d server=%s local=%s secure=%t", attempt, endpoint, conn.LocalAddr(), a.secure)
		var joinLevel uint16
		var permissions uint8
		var serverIdentity string
		if a.secure {
			var result voiceclient.SecureHandshakeResult
			result, err = voiceclient.PerformSecureHandshakeAttempts(ctx, conn, secureCodec, name, private, a.pinsPath, endpoint.String(), handshakeAttemptTimeout, handshakeAttempts, minServerVersion)
			sessionID, joinLevel, permissions = result.SessionID, result.JoinLevel, result.Permissions
			serverIdentity = result.ServerIdentity
		} else {
			sessionID, err = voiceclient.PerformHandshakeAttempts(ctx, conn, name, handshakeAttemptTimeout, handshakeAttempts, minServerVersion)
		}
		if err != nil {
			a.logger.Printf("handshake failed: attempt=%d server=%s local=%s error=%v", attempt, endpoint, conn.LocalAddr(), err)
			if ctx.Err() != nil {
				return nil
			}
			var versionError *voiceclient.ServerVersionTooOldError
			if errors.As(err, &versionError) {
				return err
			}
			var rejected *voiceclient.AuthenticationRejectedError
			if errors.As(err, &rejected) {
				return err
			}
			if shouldReplaceClientSocket(err) {
				replacement, replacementCodec, replaceErr := open()
				if replaceErr == nil {
					a.logger.Printf("replacing UDP socket: old_local=%s new_local=%s", conn.LocalAddr(), replacement.LocalAddr())
					_ = conn.Close()
					conn = replacement
					secureCodec = replacementCodec
				} else {
					a.logger.Printf("replace UDP socket failed: error=%v", replaceErr)
				}
			}
			delay := backoff.Next()
			a.events.append("reconnect", fmt.Sprintf("Сервер недоступен; повтор через %s", delay), 0)
			if !a.waitForReconnect(ctx, delay) {
				return nil
			}
			continue
		}
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
		noticeOutput := io.MultiWriter(a.output, eventWriter{log: a.events})
		sessionErr := runSession(ctx, conn, a.state, devices, a.audioDeviceChanges, name, a.commands, a.output, noticeOutput, func() { a.cancelRun() }, a.logger, !everConnected, func() {
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
		a.state.InvalidateSession(voiceclient.ConnectionReconnecting)
		if err := conn.ClearSession(sessionID); err != nil {
			return fmt.Errorf("clear UDP session: %w", err)
		}
		if secureCodec != nil {
			secureCodec.Remove(sessionID)
		}
		if !errors.Is(sessionErr, voiceclient.ErrConnectionLost) {
			return sessionErr
		}
		delay := backoff.Next()
		a.events.append("reconnect", fmt.Sprintf("Соединение потеряно (%s); повтор через %s", sessionErr, delay), 0)
		if !a.waitForReconnect(ctx, delay) {
			return nil
		}
	}
}

func (a *App) AudioDevices() (audio.DeviceList, error) { return a.listAudioDevices() }

func (a *App) AudioDeviceSelection() AudioDeviceSelection {
	a.mu.Lock()
	defer a.mu.Unlock()
	return AudioDeviceSelection{CaptureID: a.captureDeviceID, PlaybackID: a.playbackDeviceID}
}

func (a *App) SetCaptureDevice(id string) error {
	return a.setAudioDevice(id, true)
}

func (a *App) SetPlaybackDevice(id string) error {
	return a.setAudioDevice(id, false)
}

func (a *App) setAudioDevice(id string, capture bool) error {
	a.audioDeviceChangeMu.Lock()
	defer a.audioDeviceChangeMu.Unlock()

	devices, err := a.listAudioDevices()
	if err != nil {
		return err
	}
	available := devices.Playback
	if capture {
		available = devices.Capture
	}
	if id != "" {
		found := false
		for _, device := range available {
			if device.ID == id {
				found = true
				break
			}
		}
		if !found {
			return errors.New("audio device is no longer available")
		}
	}

	a.mu.Lock()
	selection := AudioDeviceSelection{CaptureID: a.captureDeviceID, PlaybackID: a.playbackDeviceID}
	changed := (capture && selection.CaptureID != id) || (!capture && selection.PlaybackID != id)
	active := a.active
	connected := a.state.ConnectionStatus() == voiceclient.ConnectionConnected
	a.mu.Unlock()
	retryCapture := capture && active && connected && !a.state.Audio.CaptureAvailable()
	if !changed && !retryCapture {
		return nil
	}
	if capture {
		selection.CaptureID = id
	} else {
		selection.PlaybackID = id
	}

	if active && connected {
		request := audioDeviceChange{selection: selection, done: make(chan error, 1)}
		timer := time.NewTimer(audioDeviceChangeTimeout)
		defer timer.Stop()
		select {
		case a.audioDeviceChanges <- request:
		case <-timer.C:
			return errors.New("timed out while applying audio device")
		case <-a.lifetimeCtx.Done():
			return ErrClosed
		}
		select {
		case err := <-request.done:
			if err != nil {
				return err
			}
		case <-timer.C:
			return errors.New("timed out while applying audio device")
		case <-a.lifetimeCtx.Done():
			return ErrClosed
		}
	}
	a.mu.Lock()
	a.captureDeviceID = selection.CaptureID
	a.playbackDeviceID = selection.PlaybackID
	a.mu.Unlock()
	if changed {
		a.events.append("audio", "Аудиоустройство изменено", 0)
		a.restartPreview(capture)
	}
	return nil
}

func (a *App) waitForReconnect(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		case command, ok := <-a.commands:
			if !ok {
				a.commands = nil
				continue
			}
			voiceclient.HandleOfflineCommand(command, a.output, func() { a.cancelRun() }, a.state)
		}
	}
}

func (a *App) cancelRun() {
	a.mu.Lock()
	cancel := a.runCancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
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
	return a.Disconnect(ctx)
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

func (a *App) Snapshot() voiceclient.ClientViewState { return a.state.SnapshotView() }

func (a *App) ConnectionStats() voiceclient.ConnectionStats { return a.state.ConnectionStats() }

func (a *App) AudioMeterSnapshot() voiceclient.AudioMeterSample {
	return a.state.Audio.AudioMeterSnapshot()
}

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

func (a *App) SetMuted(value bool) error { return a.state.Audio.SetMuted(value) }

func (a *App) SetDeafened(value bool) error { return a.state.Audio.SetDeafened(value) }

func (a *App) ParticipantVolume(id uint64) float32 { return a.state.Audio.ParticipantVolume(id) }

func (a *App) SetParticipantVolume(id uint64, value float32) error {
	if id == a.state.SessionID() {
		return errors.New("cannot change local participant playback volume")
	}
	found := false
	for _, participant := range a.state.Snapshot().Participants {
		if participant.SessionID == id {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("participant %d not found", id)
	}
	return a.state.Audio.SetParticipantVolume(id, value)
}

func (a *App) SetRNNoiseEnabled(value bool) { a.state.Audio.SetRNNoiseEnabled(value) }

func (a *App) SetRNNoiseSensitivity(value float32) error {
	return a.state.Audio.SetRNNoiseSensitivity(value)
}

func (a *App) SetMicrophoneGain(value float32) error {
	return a.state.Audio.SetMicrophoneGain(value)
}

func (a *App) SetVADEnabled(value bool) { a.state.Audio.SetVADEnabled(value) }

func (a *App) SetVADMode(value string) error {
	mode, err := voicegate.ParseMode(value)
	if err != nil {
		return err
	}
	return a.state.Audio.SetVADMode(mode)
}

func (a *App) SetVADSensitivity(value float32) error {
	return a.state.Audio.SetVADSensitivity(value)
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

func openClientPacketConn(endpoint netip.AddrPort) (*udp.ClientPacketConn, error) {
	rawConn, err := net.DialUDP("udp", nil, net.UDPAddrFromAddrPort(endpoint))
	if err != nil {
		return nil, fmt.Errorf("connect UDP: %w", err)
	}
	conn, err := udp.NewClientPacketConn(rawConn, protocol.PlainDatagramCodec{})
	if err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("configure UDP packet connection: %w", err)
	}
	return conn, nil
}

func openSecureClientPacketConn(endpoint netip.AddrPort) (*udp.ClientPacketConn, *protocol.SecureDatagramCodec, error) {
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
