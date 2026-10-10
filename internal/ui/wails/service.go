package wailsui

import (
	"context"
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
}

type Service struct {
	client           *clientapp.App
	settings         *clientsettings.Store
	settingsMu       sync.RWMutex
	displayName      string
	serverAddress    string
	trustedMediaKeys map[string]string
	theme            string
	closeToTray      bool
	connectionMu     sync.Mutex
	recentServers    []clientsettings.RecentServer
	recordedVisit    string
	serverStatus     *serverstatus.Cache
	statusChanged    func()
}

func NewService(client *clientapp.App) *Service {
	return &Service{client: client, serverStatus: serverstatus.NewCache()}
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
	s.settingsMu.Unlock()
	return nil
}

type MediaTrustDTO struct {
	Fingerprint string `json:"fingerprint"`
	Trusted     bool   `json:"trusted"`
	Known       bool   `json:"known"`
}

func (s *Service) MediaServerIdentity() (MediaTrustDTO, error) {
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	fingerprint, err := s.client.MediaServerFingerprint(ctx)
	if err != nil {
		return MediaTrustDTO{}, err
	}
	s.settingsMu.RLock()
	trusted := s.trustedMediaKeys[s.serverAddress]
	s.settingsMu.RUnlock()
	return MediaTrustDTO{Fingerprint: fingerprint, Trusted: trusted == fingerprint, Known: trusted != ""}, nil
}

func (s *Service) TrustMediaServer(fingerprint string) error {
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	actual, err := s.client.MediaServerFingerprint(ctx)
	if err != nil {
		return err
	}
	if actual != strings.TrimSpace(fingerprint) {
		return errors.New("отпечаток media-сервера изменился")
	}
	s.settingsMu.Lock()
	if s.trustedMediaKeys == nil {
		s.trustedMediaKeys = make(map[string]string)
	}
	s.trustedMediaKeys[s.serverAddress] = actual
	s.settingsMu.Unlock()
	return s.saveSettings()
}

func (s *Service) PublishScreen(offer mediasignal.SessionDescription) (clientapp.MediaPublishResult, error) {
	pin, err := s.mediaPin()
	if err != nil {
		return clientapp.MediaPublishResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return s.client.PublishScreen(ctx, offer, pin)
}

func (s *Service) SubscribeScreen(streamID, subscriberID string, offer mediasignal.SessionDescription) (clientapp.MediaSubscribeResult, error) {
	pin, err := s.mediaPin()
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
	pin := s.trustedMediaKeys[s.serverAddress]
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
	return s.client.SetParticipantVolume(id, value)
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
	dto.ChatContext = s.client.ChatContext() + "|" + dto.UserID
	s.recordServerVisit(dto)
	return dto
}

func (s *Service) ConnectionStats() ConnectionStatsDTO {
	stats := s.client.ConnectionStats()
	return ConnectionStatsDTO{
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
