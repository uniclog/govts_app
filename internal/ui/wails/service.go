package wailsui

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"uniclog.io/sonoryx/internal/clientapp"
	"uniclog.io/sonoryx/internal/clientsettings"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/hotkeys"
	"uniclog.io/sonoryx/internal/mediasignal"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/serverstatus"
)

const operationTimeout = 10 * time.Second

type validationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e *validationError) Error() string { return e.Field + ": " + e.Message }

type ConnectRequest struct {
	Name             string `json:"name"`
	Server           string `json:"server"`
	MinServerVersion string `json:"minServerVersion,omitempty"`
	SkipHistory      bool   `json:"skipHistory,omitempty"`
}

type Service struct {
	client           *clientapp.App
	settings         *clientsettings.Store
	settingsMu       sync.RWMutex
	displayName      string
	serverAddress    string
	trustedMediaKeys map[string]string
	theme            string
	locale           string
	closeToTray      bool
	// Participant volume state is guarded by settingsMu.
	rememberParticipantVolumes bool
	participantVolumes         map[string]map[string]float32
	participantVolumeSave      *time.Timer
	connectionMu               sync.Mutex
	recentServers              []clientsettings.RecentServer
	recordedVisit              string
	skipServerHistory          bool
	serverStatus               *serverstatus.Cache
	statusChanged              func()
	// audioSwitchMu orders microphone and speaker clicks with their cues.
	audioSwitchMu     sync.Mutex
	hotkeyKeys        map[string]string
	hotkeyMu          sync.Mutex
	hotkeyApplyMu     sync.Mutex
	hotkeyLifecycleMu sync.Mutex
	hotkeys           *hotkeys.Manager
	hotkeyErrors      map[string]error
	// Hotkey preferences and capture state are guarded by settingsMu.
	hotkeysStopped bool
	// hotkeyNotifier, hotkeyNotices and popupLook are guarded by hotkeyMu.
	hotkeyNotifications   bool
	hotkeysEnabled        bool
	hotkeyCapture         uint32
	hotkeyCaptureSeq      uint32
	hotkeyCaptureDeadline time.Time
	hotkeyCaptureTimer    *time.Timer
	hotkeyWindowInactive  bool
	hotkeyNotifier        *hotkeyNotifier
	hotkeyNotices         map[string]string
	popupLook             map[string]string
}

func NewService(client *clientapp.App) *Service {
	return &Service{client: client, serverStatus: serverstatus.NewCache(), hotkeyKeys: clientsettings.NormalizeHotkeys(nil), hotkeyNotifications: true}
}

// LogDiagnostic receives infrequent browser-side lifecycle events. It accepts
// bounded text only; callers must not pass SDP, credentials or media content.
func (s *Service) LogDiagnostic(operation, message string) {
	if len(operation) > 128 {
		operation = operation[:128]
	}
	if len(message) > 2048 {
		message = message[:2048]
	}
	log.Printf("frontend diagnostic: operation=%q message=%q", operation, message)
}

func (s *Service) Connect(request ConnectRequest) error {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	return s.connect(request)
}

func (s *Service) TrustServerIdentity(endpoint, fingerprint string) error {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	return s.client.TrustServerIdentity(endpoint, fingerprint)
}

func (s *Service) connect(request ConnectRequest) error {
	request.Name = strings.TrimSpace(request.Name)
	request.Server = strings.TrimSpace(request.Server)
	if request.Name == "" {
		return &validationError{Field: "name", Message: "введите имя"}
	}
	if request.Server == "" {
		return &validationError{Field: "server", Message: "введите адрес сервера"}
	}
	if _, err := clientapp.ParseServerEndpoint(request.Server); err != nil {
		return err
	}
	if connectionStatus(s.client.Snapshot().ConnectionStatus) != "disconnected" {
		return clientapp.ErrAlreadyConnected
	}
	if err := s.setDisplayName(request.Name); err != nil {
		return err
	}
	// Set before connecting so that the first snapshot already applies them.
	s.pushParticipantVolumes(request.Server)
	if err := s.client.Connect(clientapp.ConnectOptions{
		Name:             request.Name,
		Server:           request.Server,
		MinServerVersion: strings.TrimSpace(request.MinServerVersion),
	}); err != nil {
		return err
	}
	s.settingsMu.Lock()
	s.serverAddress = request.Server
	s.recordedVisit = ""
	s.skipServerHistory = request.SkipHistory
	s.settingsMu.Unlock()
	return nil
}

type MediaTrustDTO struct {
	Fingerprint         string `json:"fingerprint"`
	Trusted             bool   `json:"trusted"`
	Known               bool   `json:"known"`
	Server              string `json:"server"`
	SessionID           string `json:"sessionId"`
	Context             string `json:"context"`
	PreviousFingerprint string `json:"previousFingerprint"`
}

func (s *Service) MediaServerIdentity() (MediaTrustDTO, error) {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	return s.mediaServerIdentity()
}

func (s *Service) mediaTrustState() (MediaTrustDTO, error) {
	view := s.client.Snapshot()
	if connectionStatus(view.ConnectionStatus) != "connected" || view.SessionID == 0 {
		return MediaTrustDTO{}, errors.New("media-key-confirmation-expired")
	}
	s.settingsMu.RLock()
	server := mediaTrustServerKey(s.serverAddress)
	previous := s.mediaPinStateLocked(server)
	s.settingsMu.RUnlock()
	return MediaTrustDTO{Server: server, SessionID: formatUint64(view.SessionID), Context: s.client.ChatContext(), PreviousFingerprint: previous}, nil
}

func sameMediaTrustState(a, b MediaTrustDTO) bool {
	return a.Server == b.Server && a.SessionID == b.SessionID && a.Context == b.Context && a.PreviousFingerprint == b.PreviousFingerprint
}

func (s *Service) mediaServerIdentity() (MediaTrustDTO, error) {
	identity, err := s.mediaTrustState()
	if err != nil {
		return MediaTrustDTO{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	fingerprint, err := s.client.MediaServerFingerprint(ctx)
	if err != nil {
		return MediaTrustDTO{}, err
	}
	current, err := s.mediaTrustState()
	if err != nil || !sameMediaTrustState(identity, current) {
		return MediaTrustDTO{}, errors.New("media-key-confirmation-expired")
	}
	identity.Fingerprint = fingerprint
	identity.Trusted = identity.PreviousFingerprint == fingerprint
	identity.Known = identity.PreviousFingerprint != ""
	log.Printf("media trust check: server=%s session_id=%s saved=%q presented=%s trusted=%t", identity.Server, identity.SessionID, identity.PreviousFingerprint, fingerprint, identity.Trusted)
	return identity, nil
}

// TrustMediaServer retains first-use compatibility. Replacing an existing pin
// requires the server, session and previous fingerprint shown in the dialog.
func (s *Service) TrustMediaServer(fingerprint string) error {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	identity, err := s.mediaServerIdentity()
	if err != nil {
		return err
	}
	if identity.Fingerprint != strings.TrimSpace(fingerprint) {
		return errors.New("media-key-changed-again")
	}
	if identity.Known && !identity.Trusted {
		return errors.New("media-key-confirmation-required")
	}
	return s.confirmMediaServerIdentity(identity)
}

func (s *Service) ConfirmMediaServerIdentity(request MediaTrustDTO) error {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	return s.confirmMediaServerIdentity(request)
}

func (s *Service) confirmMediaServerIdentity(request MediaTrustDTO) error {
	current, err := s.mediaTrustState()
	if err != nil || !sameMediaTrustState(request, current) {
		return errors.New("media-key-confirmation-expired")
	}
	actual, err := s.mediaServerIdentity()
	if err != nil {
		return err
	}
	if !sameMediaTrustState(request, actual) {
		return errors.New("media-key-confirmation-expired")
	}
	if actual.Fingerprint != request.Fingerprint {
		return errors.New("media-key-changed-again")
	}
	s.settingsMu.Lock()
	previousPins := cloneStringMap(s.trustedMediaKeys)
	if s.trustedMediaKeys == nil {
		s.trustedMediaKeys = make(map[string]string)
	}
	for address := range s.trustedMediaKeys {
		if mediaTrustServerKey(address) == request.Server {
			delete(s.trustedMediaKeys, address)
		}
	}
	s.trustedMediaKeys[request.Server] = actual.Fingerprint
	s.settingsMu.Unlock()
	if err := s.saveSettings(); err != nil {
		s.settingsMu.Lock()
		s.trustedMediaKeys = previousPins
		s.settingsMu.Unlock()
		return errors.New("media-key-save-failed")
	}
	return nil
}

func (s *Service) ScreenCapabilities() (json.RawMessage, error) {
	pin, err := s.mediaPin()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return s.client.ScreenCapabilities(ctx, pin)
}

func (s *Service) PublishScreen(offer mediasignal.SessionDescription) (clientapp.MediaPublishResult, error) {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	pin, err := s.verifiedMediaPin()
	if err != nil {
		return clientapp.MediaPublishResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return s.client.PublishScreen(ctx, offer, pin)
}

func (s *Service) SubscribeScreen(streamID, subscriberID string, offer mediasignal.SessionDescription) (clientapp.MediaSubscribeResult, error) {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	pin, err := s.verifiedMediaPin()
	if err != nil {
		return clientapp.MediaSubscribeResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return s.client.SubscribeScreen(ctx, streamID, subscriberID, offer, pin)
}

func (s *Service) StopScreen(streamID string) error {
	pin, err := s.mediaPin()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.StopScreen(ctx, streamID, pin)
}
func (s *Service) UnsubscribeScreen(streamID, subscriberID string) error {
	pin, err := s.mediaPin()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.UnsubscribeScreen(ctx, streamID, subscriberID, pin)
}

func (s *Service) OpenScreenWindow(streamID, ownerName string) error {
	if _, err := parseUint64(streamID, "streamId", false); err != nil {
		return err
	}
	app := application.Get()
	if app == nil {
		return errors.New("application is not ready")
	}
	name := "screen-" + streamID
	if existing, ok := app.Window.Get(name); ok {
		existing.Focus()
		return nil
	}
	ownerName = strings.TrimSpace(ownerName)
	if ownerName == "" {
		ownerName = "Участник"
	}
	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Name:             name,
		Title:            "Демонстрация экрана — " + ownerName,
		Width:            1280,
		Height:           800,
		MinWidth:         640,
		MinHeight:        420,
		BackgroundColour: application.NewRGB(8, 12, 20),
		URL:              "/?screen=" + url.QueryEscape(streamID) + "&owner=" + url.QueryEscape(ownerName) + "&nonce=" + strconv.FormatInt(time.Now().UnixNano(), 10),
	})
	return nil
}

func (s *Service) mediaPin() (string, error) {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	pin := s.mediaPinStateLocked(s.serverAddress)
	if strings.Contains(pin, "\n") {
		return "", errors.New("media-key-confirmation-required")
	}
	if pin == "" {
		return "", errors.New("media-сервер ещё не подтверждён")
	}
	return pin, nil
}

func (s *Service) SavedDisplayName() string {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.displayName
}

func (s *Service) Theme() string {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return clientsettings.NormalizeTheme(s.theme)
}

func (s *Service) SetTheme(value string) error {
	normalized := clientsettings.NormalizeTheme(strings.TrimSpace(value))
	if normalized != value {
		return &validationError{Field: "theme", Message: "неизвестная тема"}
	}
	s.settingsMu.Lock()
	s.theme = normalized
	s.settingsMu.Unlock()
	return s.saveSettings()
}

func (s *Service) Locale() string {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	locale, ok := clientsettings.NormalizeLocale(s.locale)
	if !ok {
		return "ru"
	}
	return locale
}

func (s *Service) SetLocale(value string) error {
	value = strings.TrimSpace(value)
	locale, ok := clientsettings.NormalizeLocale(value)
	if !ok || value == "" {
		return &validationError{Field: "locale", Message: "неизвестный язык"}
	}
	s.settingsMu.Lock()
	s.locale = locale
	s.settingsMu.Unlock()
	return s.saveSettings()
}

func (s *Service) CloseToTray() bool {
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	return s.closeToTray
}

func (s *Service) SetCloseToTray(value bool) error {
	s.settingsMu.Lock()
	s.closeToTray = value
	s.settingsMu.Unlock()
	return s.saveSettings()
}

func (s *Service) ParticipantVolume(sessionID string) (float32, error) {
	id, err := parseUint64(sessionID, "sessionId", false)
	if err != nil {
		return 0, err
	}
	return s.client.ParticipantVolume(id), nil
}

func (s *Service) SetParticipantVolume(sessionID string, value float32) error {
	id, err := parseUint64(sessionID, "sessionId", false)
	if err != nil {
		return err
	}
	if err := s.client.SetParticipantVolume(id, value); err != nil {
		return err
	}
	s.saveParticipantVolume(id, value)
	return nil
}

func (s *Service) Disconnect() error {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	return s.disconnect()
}

func (s *Service) disconnect() error {
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.Disconnect(ctx)
}

func (s *Service) JoinChannel(channelID string) error {
	id, err := parseUint64(channelID, "channelId", false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.JoinChannel(ctx, domain.ChannelID(id))
}

func (s *Service) Kick(sessionID string) error {
	id, err := parseUint64(sessionID, "sessionId", false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.Moderate(ctx, protocol.PacketKick, id, 0)
}

func (s *Service) Ban(sessionID string) error {
	id, err := parseUint64(sessionID, "sessionId", false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.Moderate(ctx, protocol.PacketBan, id, 0)
}

func (s *Service) Drag(sessionID, channelID string) error {
	id, err := parseUint64(sessionID, "sessionId", false)
	if err != nil {
		return err
	}
	channel, err := parseUint64(channelID, "channelId", false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.Moderate(ctx, protocol.PacketDrag, id, domain.ChannelID(channel))
}

func (s *Service) Snapshot() ClientViewDTO {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	dto := viewDTO(s.client.Snapshot(), s.client.LastError())
	dto.ServerIdentityChange = s.client.ServerIdentityChange()
	dto.ChatContext = s.client.ChatContext() + "|" + dto.UserID
	s.recordServerVisit(dto)
	return dto
}

func (s *Service) ConnectionStats() ConnectionStatsDTO {
	stats := s.client.ConnectionStats()
	return ConnectionStatsDTO{
		Voice: VoiceStatsDTO{ReceivedCopies: stats.Voice.ReceivedCopies, SizeDrops: stats.Voice.Drops.Size, ProtocolDrops: stats.Voice.Drops.Protocol,
			Incoming:    VoiceDirectionDTO{Known: stats.Voice.RemoteKnown, Desired: stats.Voice.Remote.DesiredFrames, Allowed: stats.Voice.Remote.AllowedFrames, Actual: stats.Voice.RemoteActual, Reason: stats.Voice.Remote.Reason, Limited: stats.Voice.RemoteLimited, Restored: stats.Voice.Incoming.Restored, Late: stats.Voice.Incoming.Late, PLC: stats.Voice.Incoming.PLC},
			Outgoing:    VoiceDirectionDTO{Known: connectionStatus(stats.Status) == "connected", Desired: stats.Voice.Local.DesiredFrames, Allowed: stats.Voice.Local.AllowedFrames, Actual: stats.Voice.Actual, Reason: stats.Voice.Local.Reason, Limited: stats.Voice.Limited, Restored: stats.Voice.Outgoing.Restored, Late: stats.Voice.Outgoing.Late},
			BudgetDrops: stats.Voice.Drops.Budget, RepairBudgetDrops: stats.Voice.Drops.RepairBudget, QueueDrops: stats.Voice.Drops.Queue, WriteFailures: stats.Voice.Drops.Write,
		},
		SessionID:              formatUint64(stats.SessionID),
		Generation:             formatUint64(stats.Generation),
		Status:                 connectionStatus(stats.Status),
		PingMS:                 stats.PingMS,
		PingVariationMS:        stats.PingVariationMS,
		PingAvailable:          stats.PingAvailable,
		PingVariationAvailable: stats.PingVariationAvailable,
		PingSampleAtMS:         stats.PingSampleAtMS,
		IncomingLoss:           stats.IncomingLoss,
		IncomingKnown:          stats.IncomingKnown,
		IncomingSampleAtMS:     stats.IncomingSampleAtMS,
		OutgoingLoss:           stats.OutgoingLoss,
		OutgoingKnown:          stats.OutgoingKnown,
		RecoveredLoss:          stats.RecoveredLoss,
		ConcealedLoss:          stats.ConcealedLoss,
		LossBurstsSingle:       stats.LossBurstsSingle,
		LossBurstsDouble:       stats.LossBurstsDouble,
		LossBurstsLong:         stats.LossBurstsLong,
	}
}

func (s *Service) EventsAfter(sequence string) ([]ClientEventDTO, error) {
	after, err := parseUint64(sequence, "sequence", true)
	if err != nil {
		return nil, err
	}
	events := s.client.EventsAfter(after)
	result := make([]ClientEventDTO, 0, len(events))
	for _, event := range events {
		result = append(result, eventDTO(event))
	}
	return result, nil
}
