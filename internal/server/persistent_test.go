package server

import (
	"context"
	"path/filepath"
	"testing"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/persist"
)

func TestPersistentBootstrapKeepsChannelsAndLevels(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.db")
	ctx := context.Background()
	store, err := persist.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := BootstrapPersistentHub(ctx, store, channelSourceFunc(func(context.Context) (ServerDefinition, error) {
		return ServerDefinition{Info: domain.ServerInfo{Name: "first"}, Channels: []ChannelDefinition{{Name: "main", Default: true}, {Name: "private"}}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	channel := first.ListChannels()[1]
	if err := store.SetChannelJoinLevel(ctx, channel.ID, 25); err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	key[0] = 1
	account, err := store.FindOrCreateAccount(ctx, key, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetJoinLevel(ctx, account.ID, 25); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = persist.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	second, err := BootstrapPersistentHub(ctx, store, channelSourceFunc(func(context.Context) (ServerDefinition, error) {
		t.Fatal("JSON source was loaded again")
		return ServerDefinition{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	channels := second.ListChannels()
	if len(channels) != 2 || channels[1].ID != channel.ID || channels[1].MinJoinLevel != 25 || second.Inspect().ServerInfo.Name != "first" || second.Inspect().ServerInfo.DefaultChannelID != channels[0].ID {
		t.Fatalf("restored server = %+v, channels = %+v", second.Inspect().ServerInfo, channels)
	}
	restored, err := store.Account(ctx, account.ID)
	if err != nil || restored.JoinLevel != 25 || restored.PublicKey != key {
		t.Fatalf("restored account = %+v, %v", restored, err)
	}
}

func TestPersistentBootstrapKeepsNestedChannelIDs(t *testing.T) {
	ctx := context.Background()
	store, err := persist.Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := BootstrapPersistentHub(ctx, store, channelSourceFunc(func(context.Context) (ServerDefinition, error) {
		return ServerDefinition{Info: domain.ServerInfo{Name: "nested"}, Channels: []ChannelDefinition{{Name: "lobby", Children: []ChannelDefinition{{Name: "private", MinJoinLevel: 25}}}}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	firstChannels := first.ListChannels()
	if len(firstChannels) != 2 {
		t.Fatalf("initial channels = %+v", firstChannels)
	}
	second, err := BootstrapPersistentHub(ctx, store, channelSourceFunc(func(context.Context) (ServerDefinition, error) {
		t.Fatal("bootstrap source was read after import")
		return ServerDefinition{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	channels := second.ListChannels()
	if len(channels) != 2 || channels[0].ID != firstChannels[0].ID || channels[1].ID != firstChannels[1].ID || channels[1].ParentID != channels[0].ID || channels[1].MinJoinLevel != 25 {
		t.Fatalf("restored nested channels = %+v", channels)
	}
}
