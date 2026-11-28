package server

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"uniclog.io/sonoryx/apps/server/internal/persist"
	"uniclog.io/sonoryx/apps/server/internal/voice"
	"uniclog.io/sonoryx/internal/domain"
)

func TestConsoleDeletesChannelsAndAccounts(t *testing.T) {
	store, err := persist.Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	hub := voice.NewHub()
	if err := store.SaveInitialServer(ctx, persist.ServerState{Name: "server", DefaultChannelID: voice.DefaultChannelID, Channels: hub.ListChannels()}); err != nil {
		t.Fatal(err)
	}
	room, err := voice.NewAdministration(store, hub).CreateChannel(ctx, voice.ConsoleAdmin, domain.Channel{Name: "room"})
	if err != nil {
		t.Fatal(err)
	}
	var key [32]byte
	key[0] = 1
	account, err := store.FindOrCreateAccount(ctx, key, "alice")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	console := NewConsole(hub, strings.NewReader(""), &output, ConsoleInfo{}, store)
	run := func(line string) (string, error) {
		output.Reset()
		err := console.Execute(line)
		return output.String(), err
	}

	if help, _ := run("help"); !strings.Contains(help, "channel delete <id>") || !strings.Contains(help, "account delete <id>") {
		t.Fatalf("help = %q", help)
	}
	if got, _ := run("channel delete x"); !strings.Contains(got, "error: usage: channel delete") {
		t.Fatalf("bad channel ID output = %q", got)
	}
	if _, err := run(fmt.Sprintf("channel delete %d", voice.DefaultChannelID)); err == nil {
		t.Fatal("default channel deleted")
	}
	if got, err := run(fmt.Sprintf("channel delete %d", room.ID)); err != nil || got != fmt.Sprintf("channel %d deleted\n", room.ID) {
		t.Fatalf("channel delete = %q, %v", got, err)
	}
	if _, ok := hub.GetChannel(room.ID); ok {
		t.Fatal("deleted channel still in the hub")
	}

	if got, _ := run("account delete 0"); !strings.Contains(got, "error: usage: account delete") {
		t.Fatalf("bad account ID output = %q", got)
	}
	if got, err := run(fmt.Sprintf("account delete %d", account.ID)); err != nil || got != fmt.Sprintf("account %d deleted\n", account.ID) {
		t.Fatalf("account delete = %q, %v", got, err)
	}
	if _, err := run(fmt.Sprintf("account %d", account.ID)); err == nil {
		t.Fatal("deleted account still shown")
	}
	if _, err := run(fmt.Sprintf("account delete %d", account.ID)); err == nil {
		t.Fatal("account deleted twice")
	}
}
