package wailsui

import (
	"context"
	"strings"
	"sync"

	"uniclog.io/sonoryx/apps/desktop/internal/serverstatus"
	"uniclog.io/sonoryx/apps/desktop/internal/settings"
	clientapp "uniclog.io/sonoryx/internal/clientcore"
)

type connectionState struct {
	// connectionMu serializes operations that can change the active server.
	// History and the visible address are read and written under settingsMu.
	connectionMu      sync.Mutex
	serverAddress     string
	recentServers     []clientsettings.RecentServer
	recordedVisit     string
	skipServerHistory bool
	serverStatus      *serverstatus.Cache
	statusChanged     func()
}

type ConnectRequest struct {
	Name             string `json:"name"`
	Server           string `json:"server"`
	Password         string `json:"password,omitempty"`
	MinServerVersion string `json:"minServerVersion,omitempty"`
	SkipHistory      bool   `json:"skipHistory,omitempty"`
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
	start := func() error {
		return s.client.Connect(clientapp.ConnectOptions{
			Name:             request.Name,
			Server:           request.Server,
			Password:         request.Password,
			MinServerVersion: strings.TrimSpace(request.MinServerVersion),
		})
	}
	var err error
	if s.audio == nil {
		err = start()
	} else {
		err = s.audio.Connect(start)
	}
	if err != nil {
		return err
	}
	s.settingsMu.Lock()
	s.serverAddress = request.Server
	s.recordedVisit = ""
	s.skipServerHistory = request.SkipHistory
	s.settingsMu.Unlock()
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
