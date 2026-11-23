package persist

import (
	"context"
	"path/filepath"
	"testing"

	"uniclog.io/sonoryx/internal/domain"
)

func TestVersionEightMigrationPreservesMessagesAndAttachments(t *testing.T) {
	s, alice, bob, _ := chatTestStore(t)
	ctx := context.Background()
	target := domain.ChatTarget{Kind: domain.ChatDirect, ID: bob.ID}
	u := readyFile(t, s, alice.ID, target, 1)
	m, err := s.SaveChatFiles(ctx, alice.ID, "alice", target, [16]byte{1}, "keep me", []int64{u.AttachmentID})
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{`DROP TRIGGER chat_pins_tombstone`, `DROP TABLE chat_pins`, `DROP TRIGGER chat_reactions_tombstone`, `DROP TABLE chat_reactions`, `ALTER TABLE server_settings DROP COLUMN password`, `DROP TABLE chat_changes`, `ALTER TABLE chat_messages DROP COLUMN deleted`, `ALTER TABLE chat_message_files DROP COLUMN hidden`, `PRAGMA user_version=8`} {
		if _, err = s.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	var path string
	var sequence int
	var database string
	if err = s.db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &database, &path); err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(path) {
		t.Fatal("database path not absolute")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	history, err := migrated.ChatHistory(ctx, alice.ID, target, 0, false)
	if err != nil || len(history.Messages) != 1 || history.Messages[0].ID != m.ID || history.Messages[0].Text != "keep me" || len(history.Messages[0].AttachmentIDs) != 1 || history.Messages[0].Deleted || history.Messages[0].Revision != 0 {
		t.Fatalf("migration changed history: %+v %v", history, err)
	}
	if _, err = migrated.DeleteChat(ctx, alice.ID, target, m.ID, 0); err != nil {
		t.Fatal("migrated message cannot be deleted", err)
	}
}

func TestChatDeletionAuthorOnlyAtomicAndIdempotent(t *testing.T) {
	s, alice, bob, eve := chatTestStore(t)
	ctx := context.Background()
	target := domain.ChatTarget{Kind: domain.ChatDirect, ID: bob.ID}
	u1, u2 := readyFile(t, s, alice.ID, target, 1), readyFile(t, s, alice.ID, target, 2)
	original, err := s.SaveChatFiles(ctx, alice.ID, "alice", target, [16]byte{1}, "hello", []int64{u1.AttachmentID, u2.AttachmentID})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetOwner(ctx, bob.ID); err != nil {
		t.Fatal(err)
	}
	for _, actor := range []Account{bob, eve} {
		if _, err = s.DeleteChat(ctx, actor.ID, domain.ChatTarget{Kind: domain.ChatDirect, ID: alice.ID}, original.ID, 0); err == nil {
			t.Fatal("non-author deleted message", actor.ID)
		}
	}
	if _, err = s.DeleteChat(ctx, alice.ID, domain.ChatTarget{Kind: domain.ChatChannel, ID: 1}, original.ID, 0); err == nil {
		t.Fatal("deleted across conversations")
	}
	partial, err := s.DeleteChat(ctx, alice.ID, target, original.ID, u1.AttachmentID)
	if err != nil || partial.Deleted || partial.Text != "hello" || partial.Revision <= 0 || len(partial.AttachmentIDs) != 1 || partial.AttachmentIDs[0] != u2.AttachmentID {
		t.Fatalf("partial deletion: %+v %v", partial, err)
	}
	file, err := s.FileAttachment(ctx, u1.AttachmentID)
	if err != nil || !file.Deleted {
		t.Fatal("file access not revoked", err)
	}
	deleted, err := s.DeleteChat(ctx, alice.ID, target, original.ID, 0)
	if err != nil || !deleted.Deleted || deleted.Text != "" || len(deleted.AttachmentIDs) != 0 || deleted.Revision <= partial.Revision {
		t.Fatalf("tombstone: %+v %v", deleted, err)
	}
	watermark, err := s.ChatChangeWatermark(ctx, bob.ID, domain.ChatTarget{Kind: domain.ChatDirect, ID: alice.ID})
	if err != nil || watermark.Cursor != deleted.Revision || len(watermark.Messages) != 0 {
		t.Fatal("invalid change watermark", watermark, err)
	}
	again, err := s.DeleteChat(ctx, alice.ID, target, original.ID, 0)
	if err != nil || again.Revision != deleted.Revision {
		t.Fatal("delete not idempotent", err)
	}
	retry, err := s.SaveChatFiles(ctx, alice.ID, "alice", target, original.ClientID, "hello", []int64{u1.AttachmentID, u2.AttachmentID})
	if err != nil || !retry.Deleted {
		t.Fatal("send retry resurrected message", err)
	}
	changes, err := s.ChatChanges(ctx, bob.ID, domain.ChatTarget{Kind: domain.ChatDirect, ID: alice.ID}, 0)
	if err != nil || len(changes.Messages) != 1 || !changes.Messages[0].Deleted || changes.Cursor != deleted.Revision {
		t.Fatalf("peer changes: %+v %v", changes, err)
	}
	changes, err = s.ChatChanges(ctx, bob.ID, domain.ChatTarget{Kind: domain.ChatDirect, ID: alice.ID}, changes.Cursor)
	if err != nil || len(changes.Messages) != 0 {
		t.Fatal("revision cursor repeated changes", err)
	}
	for _, upload := range []FileUpload{u1, u2} {
		state, err := s.FileUpload(ctx, upload.ID)
		if err != nil || state.State != "expired" {
			t.Fatal("file quota not released", err)
		}
	}
}

func TestChatFileDeletionRollbackAndLastAttachmentTombstone(t *testing.T) {
	s, alice, bob, _ := chatTestStore(t)
	ctx := context.Background()
	target := domain.ChatTarget{Kind: domain.ChatDirect, ID: bob.ID}
	u := readyFile(t, s, alice.ID, target, 1)
	m, err := s.SaveChatFiles(ctx, alice.ID, "alice", target, [16]byte{1}, "\u2003 \n\t", []int64{u.AttachmentID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.Exec(`CREATE TRIGGER reject_delete BEFORE UPDATE OF deleted ON chat_messages BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteChat(ctx, alice.ID, target, m.ID, u.AttachmentID); err == nil {
		t.Fatal("accepted injected storage failure")
	}
	a, err := s.FileAttachment(ctx, u.AttachmentID)
	if err != nil || a.Deleted {
		t.Fatal("failed transaction revoked file", err)
	}
	history, err := s.ChatHistory(ctx, alice.ID, target, 0, false)
	if err != nil || len(history.Messages) != 1 || len(history.Messages[0].AttachmentIDs) != 1 || history.Messages[0].Revision != 0 {
		t.Fatalf("failed transaction changed history: %+v %v", history, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER reject_delete`); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.DeleteChat(ctx, alice.ID, target, m.ID, u.AttachmentID)
	if err != nil || !deleted.Deleted {
		t.Fatalf("last file left empty message: %+v %v", deleted, err)
	}
}
