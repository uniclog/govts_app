package wailsui

import (
	"context"
	"net/netip"
	"strings"
	"sync"
	"time"

	"uniclog.io/govts/internal/clientapp"
)

type ServerPopulationDTO struct {
	Address string `json:"address"`
	Clients uint32 `json:"clients"`
	Online  bool   `json:"online"`
}

func (s *Service) ServerPopulations(addresses []string) []ServerPopulationDTO {
	const maxServers = 64
	if len(addresses) > maxServers {
		addresses = addresses[:maxServers]
	}
	type job struct {
		address  string
		endpoint netip.AddrPort
		valid    bool
	}
	seen := make(map[string]struct{}, len(addresses))
	jobs := make([]job, 0, len(addresses))
	for _, raw := range addresses {
		address := strings.TrimSpace(raw)
		if address == "" {
			continue
		}
		if _, exists := seen[address]; exists {
			continue
		}
		seen[address] = struct{}{}
		endpoint, err := clientapp.ParseServerEndpoint(address)
		jobs = append(jobs, job{address: address, endpoint: endpoint, valid: err == nil})
	}
	result := make([]ServerPopulationDTO, len(jobs))
	var wg sync.WaitGroup
	for i, entry := range jobs {
		result[i].Address = entry.address
		if !entry.valid {
			continue
		}
		wg.Add(1)
		go func(i int, endpoint netip.AddrPort) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 1400*time.Millisecond)
			defer cancel()
			count, err := clientapp.ProbeServerPopulation(ctx, endpoint)
			if err != nil {
				return
			}
			result[i].Online = true
			result[i].Clients = count
		}(i, entry.endpoint)
	}
	wg.Wait()
	return result
}
