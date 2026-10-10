package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"uniclog.io/sonoryx/internal/domain"
)

type channelSourceFunc func(context.Context) (ServerDefinition, error)

func (function channelSourceFunc) Load(ctx context.Context) (ServerDefinition, error) {
	return function(ctx)
}

func TestBootstrapHubUsesOnlyBuiltinDefaultChannel(t *testing.T) {
	hub, err := BootstrapHub(context.Background(), BuiltinBootstrapSource{})
	if err != nil {
		t.Fatal(err)
	}
	channels := hub.ListChannels()
	if len(channels) != 1 {
		t.Fatalf("channel count = %d, want 1", len(channels))
	}
	defaultChannel := channels[0]
	if defaultChannel.ID != 1 || defaultChannel.Name != "default" || defaultChannel.ParentID != 0 {
		t.Fatalf("default channel = %+v", defaultChannel)
	}

	alice, err := hub.CreateSession("alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := hub.CreateSession("bob", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := hub.JoinChannel(alice.ID, defaultChannel.ID); err != nil {
		t.Fatal(err)
	}
	if err := hub.JoinChannel(bob.ID, defaultChannel.ID); err != nil {
		t.Fatal(err)
	}
	recipients, err := hub.RecipientsFor(alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(recipients) != 1 || recipients[0].ID != bob.ID {
		t.Fatalf("voice recipients = %+v, want Bob", recipients)
	}
}

func TestBootstrapSelectsMarkedDefaultRatherThanFirstChannel(t *testing.T) {
	path := writeTestConfig(t, `{"server":{"name":"Test Server"},"channels":[{"name":"private","min_join_level":25},{"name":"welcome","default":true},{"name":"other"}]}`)
	hub, err := BootstrapHub(context.Background(), JSONBootstrapSource{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	if got := hub.Inspect().ServerInfo.DefaultChannelID; got != 2 {
		t.Fatalf("default channel ID = %d, want 2", got)
	}
	if _, err := hub.SetChannelJoinLevel(2, 25); err == nil {
		t.Fatal("default channel accepted a restricted join level")
	}
}

func TestBootstrapRejectsRestrictedDefault(t *testing.T) {
	path := writeTestConfig(t, `{"server":{"name":"Test Server"},"channels":[{"name":"welcome","default":true,"max_users":10}]}`)
	if _, err := BootstrapHub(context.Background(), JSONBootstrapSource{Path: path}); err == nil {
		t.Fatal("limited default channel was accepted")
	}
}

func TestJSONBootstrapSourceBuildsParentFirstTree(t *testing.T) {
	path := writeTestConfig(t, `{
  "server": {"name": "Test Server"},
  "channels": [
    {
      "name": "main",
      "topic": "Main topic",
      "description": "Main description",
      "position": 20,
      "max_users": 50,
      "children": [
        {"name": "gaming", "position": 10, "max_users": 10},
        {"name": "music", "position": 20}
      ]
    },
    {"name": "afk", "position": 10}
  ]
}`)

	hub, err := BootstrapHub(context.Background(), JSONBootstrapSource{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	main := testChannelNamed(t, hub.ListChannels(), "main")
	gaming := testChannelNamed(t, hub.ListChannels(), "gaming")
	music := testChannelNamed(t, hub.ListChannels(), "music")
	afk := testChannelNamed(t, hub.ListChannels(), "afk")

	if main.ID != 1 || gaming.ID != 2 || music.ID != 3 || afk.ID != 4 {
		t.Fatalf("parent-first IDs = main:%d gaming:%d music:%d afk:%d", main.ID, gaming.ID, music.ID, afk.ID)
	}
	if gaming.ParentID != main.ID || music.ParentID != main.ID || afk.ParentID != 0 {
		t.Fatalf("unexpected parents: gaming=%d music=%d afk=%d", gaming.ParentID, music.ParentID, afk.ParentID)
	}
	if main.Topic != "Main topic" || main.Description != "Main description" || main.MaxUsers != 50 {
		t.Fatalf("main metadata = %+v", main)
	}

	channels := hub.ListChannels()
	wantIDs := []domain.ChannelID{afk.ID, main.ID, gaming.ID, music.ID}
	for index, wantID := range wantIDs {
		if channels[index].ID != wantID {
			t.Fatalf("sorted channel[%d] ID = %d, want %d", index, channels[index].ID, wantID)
		}
	}
}

func TestJSONBootstrapSourceAppliesChannelAudioProfile(t *testing.T) {
	path := writeTestConfig(t, `{
  "server": {"name": "Test Server"},
  "channels": [{
    "name": "high-quality",
    "audio": {
      "codec": "opus",
      "sample_rate": 48000,
      "channels": 1,
      "frame_duration_ms": 20,
      "bitrate": 48000,
      "application": "audio"
    }
  }]
}`)

	hub, err := BootstrapHub(context.Background(), JSONBootstrapSource{Path: path})
	if err != nil {
		t.Fatal(err)
	}
	channel := testChannelNamed(t, hub.ListChannels(), "high-quality")
	if channel.Audio.Bitrate != 48_000 || channel.Audio.Application != domain.OpusApplicationAudio {
		t.Fatalf("audio profile = %+v", channel.Audio)
	}
}

func TestJSONBootstrapSourceRejectsUnsupportedChannelAudioProfile(t *testing.T) {
	path := writeTestConfig(t, `{
  "server": {"name": "Test Server"},
  "channels": [{"name": "invalid", "audio": {"bitrate": 1000}}]
}`)

	if _, err := BootstrapHub(context.Background(), JSONBootstrapSource{Path: path}); err == nil || !strings.Contains(err.Error(), "bitrate") {
		t.Fatalf("BootstrapHub() error = %v, want bitrate validation error", err)
	}
}

func TestJSONBootstrapSourceRejectsEmptyChannelList(t *testing.T) {
	path := writeTestConfig(t, `{"server":{"name":"Test Server"},"channels": []}`)
	if hub, err := BootstrapHub(context.Background(), JSONBootstrapSource{Path: path}); hub != nil || err == nil || !strings.Contains(err.Error(), "at least one channel") {
		t.Fatalf("BootstrapHub() = (%v, %v), want empty-channel error", hub, err)
	}
}

func TestJSONBootstrapSourceRejectsInvalidDocuments(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "empty", content: "", want: "decode server config"},
		{name: "malformed", content: `{`, want: "decode server config"},
		{name: "unknown root field", content: `{"unknown": true}`, want: "unknown field"},
		{name: "unknown channel field", content: `{"channels":[{"name":"main","unknown":true}]}`, want: "unknown field"},
		{name: "missing server name", content: `{"channels":[]}`, want: "server name"},
		{name: "untrimmed server name", content: `{"server":{"name":" test "},"channels":[]}`, want: "server name"},
		{name: "second document", content: `{"channels":[]} {"channels":[]}`, want: "multiple JSON documents"},
		{name: "trailing garbage", content: `{"channels":[]} trailing`, want: "trailing data"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := writeTestConfig(t, test.content)
			_, err := (JSONBootstrapSource{Path: path}).Load(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestJSONBootstrapSourceRejectsOversizedFile(t *testing.T) {
	path := writeTestConfig(t, strings.Repeat(" ", MaxServerConfigBytes+1))
	_, err := (JSONBootstrapSource{Path: path}).Load(context.Background())
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("Load() error = %v, want size error", err)
	}
}

func TestBootstrapHubRejectsInvalidDefinitionsAtomically(t *testing.T) {
	tooMany := make([]ChannelDefinition, MaxBootstrapChannels+1)
	for index := range tooMany {
		tooMany[index].Name = "channel-" + strings.Repeat("x", index%8) + string(rune('a'+index%26))
		tooMany[index].Position = uint32(index)
	}
	tooDeep := ChannelDefinition{Name: "depth-1"}
	cursor := &tooDeep
	for depth := 2; depth <= domain.MaxChannelDepth+1; depth++ {
		cursor.Children = []ChannelDefinition{{Name: "depth"}}
		cursor = &cursor.Children[0]
	}

	tests := []struct {
		name        string
		definitions []ChannelDefinition
		want        string
	}{
		{
			name: "duplicate sibling",
			definitions: []ChannelDefinition{
				{Name: "main"},
				{Name: "MAIN"},
			},
			want: "channels[1]",
		},
		{name: "invalid metadata", definitions: []ChannelDefinition{{Name: " main"}}, want: "channels[0]"},
		{name: "too deep", definitions: []ChannelDefinition{tooDeep}, want: "depth exceeds"},
		{name: "too many", definitions: tooMany, want: "count exceeds"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := channelSourceFunc(func(context.Context) (ServerDefinition, error) {
				return ServerDefinition{Info: domain.ServerInfo{Name: "Test Server"}, Channels: test.definitions}, nil
			})
			hub, err := BootstrapHub(context.Background(), source)
			if hub != nil {
				t.Fatalf("BootstrapHub() returned partial Hub: %+v", hub.ListChannels())
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("BootstrapHub() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestBootstrapHubPropagatesContextAndSourceErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if hub, err := BootstrapHub(ctx, BuiltinBootstrapSource{}); hub != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled BootstrapHub() = (%v, %v)", hub, err)
	}

	wantErr := errors.New("source failed")
	source := channelSourceFunc(func(context.Context) (ServerDefinition, error) {
		return ServerDefinition{}, wantErr
	})
	if hub, err := BootstrapHub(context.Background(), source); hub != nil || !errors.Is(err, wantErr) {
		t.Fatalf("failed BootstrapHub() = (%v, %v)", hub, err)
	}
}

func testChannelNamed(t *testing.T, channels []domain.Channel, name string) domain.Channel {
	t.Helper()
	for _, channel := range channels {
		if channel.Name == name {
			return channel
		}
	}
	t.Fatalf("channel %q not found", name)
	return domain.Channel{}
}

func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "server.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
