package wailsui

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	clientapp "uniclog.io/sonoryx/internal/clientcore"
	"uniclog.io/sonoryx/internal/mediasignal"
)

type MediaTrustDTO struct {
	sessionID           uint64
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
	return MediaTrustDTO{sessionID: view.SessionID, Server: server, SessionID: formatUint64(view.SessionID), Context: s.client.ChatContext(), PreviousFingerprint: previous}, nil
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
	signaling, err := s.client.MediaSignaling(identity.sessionID)
	if err != nil {
		return MediaTrustDTO{}, err
	}
	fingerprint, err := signaling.Fingerprint(ctx)
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
	s.settingsSaveMu.Lock()
	defer s.settingsSaveMu.Unlock()
	var previousPins map[string]string
	if !s.client.WithCurrentSession(actual.sessionID, func() {
		s.settingsMu.Lock()
		defer s.settingsMu.Unlock()
		previousPins = cloneStringMap(s.trustedMediaKeys)
		if s.trustedMediaKeys == nil {
			s.trustedMediaKeys = make(map[string]string)
		}
		for address := range s.trustedMediaKeys {
			if mediaTrustServerKey(address) == request.Server {
				delete(s.trustedMediaKeys, address)
			}
		}
		s.trustedMediaKeys[request.Server] = actual.Fingerprint
	}) {
		return errors.New("media-key-confirmation-expired")
	}
	if err := s.saveSettingsLocked(); err != nil {
		s.settingsMu.Lock()
		s.trustedMediaKeys = previousPins
		s.settingsMu.Unlock()
		return errors.New("media-key-save-failed")
	}
	return nil
}

func (s *Service) ScreenCapabilities() (json.RawMessage, error) {
	sessionID, pin, err := s.mediaPin()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	signaling, err := s.client.MediaSignaling(sessionID)
	if err != nil {
		return nil, err
	}
	return signaling.Capabilities(ctx, pin)
}

func (s *Service) PublishScreen(offer mediasignal.SessionDescription) (clientapp.MediaPublishResult, error) {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	trust, err := s.verifiedMediaPin()
	if err != nil {
		return clientapp.MediaPublishResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	signaling, err := s.client.MediaSignaling(trust.sessionID)
	if err != nil {
		return clientapp.MediaPublishResult{}, err
	}
	return signaling.Publish(ctx, offer, trust.Fingerprint)
}

func (s *Service) SubscribeScreen(streamID, subscriberID string, offer mediasignal.SessionDescription) (clientapp.MediaSubscribeResult, error) {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	trust, err := s.verifiedMediaPin()
	if err != nil {
		return clientapp.MediaSubscribeResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	signaling, err := s.client.MediaSignaling(trust.sessionID)
	if err != nil {
		return clientapp.MediaSubscribeResult{}, err
	}
	return signaling.Subscribe(ctx, streamID, subscriberID, offer, trust.Fingerprint)
}

func (s *Service) StopScreen(streamID string) error {
	sessionID, pin, err := s.mediaPin()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.StopScreen(ctx, sessionID, streamID, pin)
}
func (s *Service) UnsubscribeScreen(streamID, subscriberID string) error {
	sessionID, pin, err := s.mediaPin()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	signaling, err := s.client.MediaSignaling(sessionID)
	if err != nil {
		return err
	}
	return signaling.Unsubscribe(ctx, streamID, subscriberID, pin)
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

func (s *Service) mediaPin() (uint64, string, error) {
	sessionID := s.client.Snapshot().SessionID
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	pin := s.mediaPinStateLocked(s.serverAddress)
	if strings.Contains(pin, "\n") {
		return 0, "", errors.New("media-key-confirmation-required")
	}
	if pin == "" {
		return 0, "", errors.New("media-сервер ещё не подтверждён")
	}
	return sessionID, pin, nil
}
