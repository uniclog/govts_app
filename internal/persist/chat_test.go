package persist

import (
	"context"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"uniclog.io/sonoryx/internal/domain"
)

func chatTestStore(t *testing.T) (*Store, Account, Account, Account) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "chat.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if err := s.SaveInitialServer(ctx, ServerState{Name: "server", DefaultChannelID: 1, Channels: []domain.Channel{{ID: 1, Name: "main", Type: domain.ChannelTypePermanent, Audio: domain.DefaultAudioProfile()}}}); err != nil {
		t.Fatal(err)
	}
	accounts := make([]Account, 3)
	for i := range accounts {
		accounts[i], err = s.FindOrCreateAccount(ctx, [32]byte{byte(i + 1)}, []string{"alice", "bob", "eve"}[i])
		if err != nil {
			t.Fatal(err)
		}
	}
	return s, accounts[0], accounts[1], accounts[2]
}

func TestChatSendIsIdempotentUnderConcurrentRetries(t *testing.T) {
	s, alice, bob, _ := chatTestStore(t)
	target := domain.ChatTarget{Kind: domain.ChatDirect, ID: bob.ID}
	const count = 8
	var wg sync.WaitGroup
	messages := make([]domain.ChatMessage, count)
	errs := make([]error, count)
	for i := range messages {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			messages[i], errs[i] = s.SaveChat(context.Background(), alice.ID, "alice", target, [16]byte{1}, "first\nsecond")
		}(i)
	}
	wg.Wait()
	for i := range messages {
		if errs[i] != nil || !reflect.DeepEqual(messages[i], messages[0]) {
			t.Fatalf("retry %d: %+v, %v", i, messages[i], errs[i])
		}
	}
	if _, err := s.SaveChat(context.Background(), alice.ID, "alice", target, [16]byte{1}, "different"); err == nil {
		t.Fatal("accepted reused clientId with different text")
	}
	p, err := s.ChatHistory(context.Background(), bob.ID, domain.ChatTarget{Kind: domain.ChatDirect, ID: alice.ID}, 0, false)
	if err != nil || len(p.Messages) != 1 || p.Unread != 1 {
		t.Fatalf("history %+v, %v", p, err)
	}
}

func TestChatHistoryIsolationAndReadMarkers(t *testing.T) {
	s, alice, bob, eve := chatTestStore(t)
	ctx := context.Background()
	target := domain.ChatTarget{Kind: domain.ChatDirect, ID: bob.ID}
	first, err := s.SaveChat(ctx, alice.ID, "alice", target, [16]byte{1}, "one")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.SaveChat(ctx, alice.ID, "alice", target, [16]byte{2}, "two")
	if err != nil {
		t.Fatal(err)
	}
	peer := domain.ChatTarget{Kind: domain.ChatDirect, ID: alice.ID}
	p, err := s.ChatHistory(ctx, eve.ID, peer, 0, false)
	if err != nil || len(p.Messages) != 0 {
		t.Fatalf("third party history %+v, %v", p, err)
	}
	p, err = s.ChatHistory(ctx, bob.ID, peer, second.ID, false)
	if err != nil || len(p.Messages) != 1 || p.Messages[0].ID != first.ID {
		t.Fatalf("older cursor %+v, %v", p, err)
	}
	p, err = s.ChatHistory(ctx, bob.ID, peer, first.ID, true)
	if err != nil || len(p.Messages) != 1 || p.Messages[0].ID != second.ID {
		t.Fatalf("forward cursor %+v, %v", p, err)
	}
	for _, id := range []int64{first.ID, second.ID + 999, first.ID} {
		if err := s.ReadChat(ctx, bob.ID, peer, id); err != nil {
			t.Fatal(err)
		}
	}
	p, err = s.ChatHistory(ctx, bob.ID, peer, 0, false)
	if err != nil || p.ReadID != second.ID || p.Unread != 0 {
		t.Fatalf("read marker %+v, %v", p, err)
	}
	dialogs, err := s.ChatDialogs(ctx, bob.ID, 0)
	if err != nil || len(dialogs.Dialogs) != 1 || dialogs.Dialogs[0].UserID != alice.ID || dialogs.Dialogs[0].ReadID != second.ID {
		t.Fatalf("dialogs %+v, %v", dialogs, err)
	}
	dialogs, err = s.ChatDialogs(ctx, eve.ID, 0)
	if err != nil || len(dialogs.Dialogs) != 0 {
		t.Fatalf("third party dialogs %+v, %v", dialogs, err)
	}
}

func TestChatChannelHistorySurvivesReopen(t *testing.T) {
	s, alice, _, _ := chatTestStore(t)
	ctx := context.Background()
	m, err := s.SaveChat(ctx, alice.ID, "alice", domain.ChatTarget{Kind: domain.ChatChannel, ID: 1}, [16]byte{1}, "persistent")
	if err != nil {
		t.Fatal(err)
	}
	var sequence int
	var name, path string
	if err := s.db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p, err := s.ChatHistory(ctx, alice.ID, domain.ChatTarget{Kind: domain.ChatChannel, ID: 1}, 0, false)
	if err != nil || len(p.Messages) != 1 || p.Messages[0] != m || p.Unread != 0 {
		t.Fatalf("channel history %+v, %v", p, err)
	}
}

func TestChatSendRejectsUnavailableAccounts(t *testing.T) {
	s, alice, bob, _ := chatTestStore(t)
	ctx := context.Background()
	for _, id := range []int64{alice.ID, 99999} {
		if _, err := s.SaveChat(ctx, alice.ID, "alice", domain.ChatTarget{Kind: domain.ChatDirect, ID: id}, [16]byte{1}, "hello"); err == nil {
			t.Fatalf("accepted recipient %d", id)
		}
	}
	if err := s.SetBan(ctx, "test", bob.ID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveChat(ctx, alice.ID, "alice", domain.ChatTarget{Kind: domain.ChatDirect, ID: bob.ID}, [16]byte{2}, "hello"); err == nil {
		t.Fatal("accepted banned recipient")
	}
	if err := s.SetBan(ctx, "test", alice.ID, true, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveChat(ctx, alice.ID, "alice", domain.ChatTarget{Kind: domain.ChatChannel, ID: 1}, [16]byte{3}, "hello"); err == nil {
		t.Fatal("accepted banned sender")
	}
}
