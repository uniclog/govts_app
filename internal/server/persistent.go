package server

import (
	"context"
	"fmt"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/persist"
	"uniclog.io/sonoryx/internal/voice"
)

// BootstrapPersistentHub imports the JSON/builtin definition exactly once.
// Subsequent starts restore the same channel IDs and stored server name.
func BootstrapPersistentHub(ctx context.Context, store *persist.Store, source BootstrapSource) (*voice.Hub, error) {
	state, initialized, err := store.LoadServer(ctx)
	if err != nil {
		return nil, fmt.Errorf("load server state: %w", err)
	}
	if !initialized {
		hub, err := BootstrapHub(ctx, source)
		if err != nil {
			return nil, err
		}
		info := hub.Inspect().ServerInfo
		state = persist.ServerState{Name: info.Name, DefaultChannelID: info.DefaultChannelID, Channels: hub.ListChannels()}
		if err := store.SaveInitialServer(ctx, state); err != nil {
			return nil, fmt.Errorf("save initial server state: %w", err)
		}
		return hub, nil
	}
	if len(state.Channels) == 0 {
		return nil, fmt.Errorf("stored server has no channels")
	}
	hub, err := voice.NewEmptyHubWithServerInfo(domain.ServerInfo{Name: state.Name})
	if err != nil {
		return nil, err
	}
	for _, channel := range state.Channels {
		if err := hub.RestoreChannel(channel); err != nil {
			return nil, fmt.Errorf("restore channel %d: %w", channel.ID, err)
		}
	}
	if err := hub.SetDefaultChannel(state.DefaultChannelID); err != nil {
		return nil, fmt.Errorf("restore default channel: %w", err)
	}
	return hub, nil
}
