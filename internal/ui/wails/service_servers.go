package wailsui

import (
	"errors"
	"sort"
	"strings"
	"time"

	"uniclog.io/sonoryx/internal/clientapp"
	"uniclog.io/sonoryx/internal/clientsettings"
	"uniclog.io/sonoryx/internal/serverstatus"
)

type RecentServerDTO struct {
	clientsettings.RecentServer
	serverstatus.Status
	Current bool `json:"current"`
}

func (s *Service) recordServerVisit(view ClientViewDTO) {
	if view.ConnectionStatus != "connected" || !view.SnapshotFresh {
		return
	}
	s.settingsMu.Lock()
	address := s.serverAddress
	endpoint, err := clientapp.ParseServerEndpoint(address)
	if err != nil {
		s.settingsMu.Unlock()
		return
	}
	address = endpoint.String()
	visit := address + "|" + view.ChatContext + "|" + view.SessionID
	if s.recordedVisit == visit {
		s.settingsMu.Unlock()
		return
	}
	s.recordedVisit = visit
	index := -1
	for i := range s.recentServers {
		if s.recentServers[i].Address == address {
			index = i
			break
		}
	}
	if index < 0 {
		s.recentServers = append(s.recentServers, clientsettings.RecentServer{Address: address})
		index = len(s.recentServers) - 1
	}
	s.recentServers[index].LastVisited = time.Now().UnixMilli()
	sort.SliceStable(s.recentServers, func(i, j int) bool {
		left, right := s.recentServers[i], s.recentServers[j]
		if left.Favorite != right.Favorite {
			return left.Favorite
		}
		return left.LastVisited > right.LastVisited
	})
	// Keep favorites and the thirty most recently visited unpinned servers.
	unpinned := 0
	kept := s.recentServers[:0]
	for _, server := range s.recentServers {
		if !server.Favorite {
			unpinned++
			if unpinned > 30 {
				continue
			}
		}
		kept = append(kept, server)
	}
	s.recentServers = kept
	s.settingsMu.Unlock()
	_ = s.saveSettings()
}

func (s *Service) RecentServers() []RecentServerDTO {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	view := viewDTO(s.client.Snapshot(), s.client.LastError())
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	endpoint, _ := clientapp.ParseServerEndpoint(s.serverAddress)
	result := make([]RecentServerDTO, 0, len(s.recentServers))
	for _, server := range s.recentServers {
		address, err := clientapp.ParseServerEndpoint(server.Address)
		status := serverstatus.Status{Status: "unknown"}
		if s.serverStatus != nil {
			status = s.serverStatus.Snapshot(address)
		}
		if err != nil {
			status = serverstatus.Status{Status: "unavailable"}
		}
		current := err == nil && view.ConnectionStatus == "connected" && address == endpoint
		if current && view.SnapshotFresh {
			count := uint32(len(view.Participants))
			status = serverstatus.Status{OnlineCount: &count, Status: "available"}
		} else if current {
			status = serverstatus.Status{Status: "unavailable"}
			if len(view.Participants) > 0 {
				count := uint32(len(view.Participants))
				status.OnlineCount = &count
			}
		}
		result = append(result, RecentServerDTO{RecentServer: server, Current: current, Status: status})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Favorite != result[j].Favorite {
			return result[i].Favorite
		}
		return result[i].LastVisited > result[j].LastVisited
	})
	return result
}

func (s *Service) SetServerFavorite(address string, favorite bool) error {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	s.settingsMu.Lock()
	found := false
	for i := range s.recentServers {
		if s.recentServers[i].Address == address {
			s.recentServers[i].Favorite = favorite
			found = true
			break
		}
	}
	s.settingsMu.Unlock()
	if !found {
		return errors.New("сервер отсутствует в истории")
	}
	return s.saveSettings()
}

func (s *Service) DeleteRecentServer(address string) error {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	s.settingsMu.Lock()
	found := false
	for i := range s.recentServers {
		if s.recentServers[i].Address == address {
			s.recentServers = append(s.recentServers[:i], s.recentServers[i+1:]...)
			found = true
			break
		}
	}
	s.settingsMu.Unlock()
	if !found {
		return errors.New("сервер отсутствует в истории")
	}
	if s.serverStatus != nil {
		endpoint, _ := clientapp.ParseServerEndpoint(address)
		s.serverStatus.Forget(endpoint)
	}
	return s.saveSettings()
}

func (s *Service) SetServerAlias(address, alias string) error {
	alias = strings.TrimSpace(alias)
	if len([]rune(alias)) > 64 {
		return errors.New("название сервера должно быть не длиннее 64 символов")
	}
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	s.settingsMu.Lock()
	found := false
	for i := range s.recentServers {
		if s.recentServers[i].Address == address {
			s.recentServers[i].Alias = alias
			found = true
			break
		}
	}
	s.settingsMu.Unlock()
	if !found {
		return errors.New("сервер отсутствует в истории")
	}
	return s.saveSettings()
}

func (s *Service) ReconnectServer(address string) error {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	endpoint, err := clientapp.ParseServerEndpoint(strings.TrimSpace(address))
	if err != nil {
		return err
	}
	view := viewDTO(s.client.Snapshot(), s.client.LastError())
	if view.ConnectionStatus != "connected" {
		return errors.New("дождитесь подключения к серверу")
	}
	s.settingsMu.RLock()
	current, _ := clientapp.ParseServerEndpoint(s.serverAddress)
	name := s.displayName
	found := false
	for _, server := range s.recentServers {
		if server.Address == endpoint.String() {
			found = true
			break
		}
	}
	s.settingsMu.RUnlock()
	if !found {
		return errors.New("сервер отсутствует в истории")
	}
	if current == endpoint {
		return nil
	}
	for _, participant := range view.Participants {
		if participant.Local {
			name = participant.DisplayName
			break
		}
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("не удалось определить текущий ник")
	}
	if err := s.disconnect(); err != nil {
		return err
	}
	return s.connect(ConnectRequest{Name: name, Server: endpoint.String()})
}
