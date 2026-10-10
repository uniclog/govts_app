package wailsui

import (
	"net/netip"

	"github.com/wailsapp/wails/v3/pkg/application"
	"uniclog.io/sonoryx/internal/clientapp"
)

// ConfigureServerStatuses installs the completion event without starting a worker.
func ConfigureServerStatuses(app *application.App, s *Service) func() {
	s.statusChanged = func() { app.Event.Emit("server-status-changed", true) }
	return s.serverStatus.Close
}

func (s *Service) RefreshServerStatuses() error {
	// Reserve entries while history mutations are excluded; release all service
	// locks before waiting for the network.
	s.connectionMu.Lock()
	addresses, current := s.statusTargets()
	batch, err := s.serverStatus.Begin(addresses, current)
	s.connectionMu.Unlock()
	if err != nil {
		return err
	}
	if err := batch.Run(); err != nil {
		return err
	}
	if s.statusChanged != nil {
		s.statusChanged()
	}
	return nil
}

func (s *Service) statusTargets() ([]netip.AddrPort, netip.AddrPort) {
	view := s.client.Snapshot()
	s.settingsMu.RLock()
	defer s.settingsMu.RUnlock()
	var current netip.AddrPort
	if connectionStatus(view.ConnectionStatus) == "connected" && view.SnapshotFresh {
		current, _ = clientapp.ParseServerEndpoint(s.serverAddress)
	}
	addresses := make([]netip.AddrPort, 0, len(s.recentServers))
	for _, server := range s.recentServers {
		address, err := clientapp.ParseServerEndpoint(server.Address)
		if err == nil {
			addresses = append(addresses, address)
		}
	}
	return addresses, current
}
