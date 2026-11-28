package persist

import (
	"context"
	"strings"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

func TestAdminFileDeletionPublishesBoundedChatChange(t *testing.T) {
	s, alice, bob, _ := chatTestStore(t)
	ctx := context.Background()
	target := domain.ChatTarget{Kind: domain.ChatDirect, ID: bob.ID}
	var ids []int64
	for i := 1; i <= 4; i++ {
		ids = append(ids, readyFile(t, s, alice.ID, target, i).AttachmentID)
	}
	m, err := s.SaveChatFiles(ctx, alice.ID, strings.Repeat("a", 64), target, [16]byte{1}, strings.Repeat("x", 1000), ids)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DeleteFile(ctx, "local-console", ids[0]); err != nil {
		t.Fatal(err)
	}
	peer := domain.ChatTarget{Kind: domain.ChatDirect, ID: alice.ID}
	page, err := s.ChatChanges(ctx, bob.ID, peer, 0)
	if err != nil || len(page.Messages) != 1 {
		t.Fatal("missing admin deletion change", page, err)
	}
	changed := page.Messages[0]
	if changed.ID != m.ID || changed.Deleted || changed.Text != m.Text || len(changed.AttachmentIDs) != 3 || changed.Revision <= 0 {
		t.Fatal("wrong admin change", changed)
	}
	for _, id := range changed.AttachmentIDs {
		if id == ids[0] {
			t.Fatal("deleted file still visible")
		}
	}
	if _, err = protocol.EncodeChatPage(page); err != nil {
		t.Fatal("maximum changed message does not fit datagram", err)
	}
	if err = s.ExpireFiles(ctx, time.Hour, 0); err != nil {
		t.Fatal(err)
	}
	again, err := s.ChatChanges(ctx, bob.ID, peer, page.Cursor)
	if err != nil || len(again.Messages) != 0 {
		t.Fatal("cleanup repeats existing change", again, err)
	}
}

func TestFileCleanupPublishesRetentionAndLegacyRevocations(t *testing.T) {
	for _, reason := range []string{"retention", "legacy"} {
		t.Run(reason, func(t *testing.T) {
			s, alice, bob, _ := chatTestStore(t)
			ctx := context.Background()
			target := domain.ChatTarget{Kind: domain.ChatDirect, ID: bob.ID}
			u := readyFile(t, s, alice.ID, target, 1)
			m, err := s.SaveChatFiles(ctx, alice.ID, "alice", target, [16]byte{1}, "\u2003 \n\t", []int64{u.AttachmentID})
			if err != nil {
				t.Fatal(err)
			}
			retention := time.Duration(0)
			if reason == "retention" {
				retention = time.Second
				_, err = s.db.Exec(`UPDATE file_attachments SET created=1 WHERE id=?`, u.AttachmentID)
			} else {
				_, err = s.db.Exec(`UPDATE file_attachments SET deleted=1 WHERE id=?`, u.AttachmentID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = s.ExpireFiles(ctx, time.Hour, retention); err != nil {
				t.Fatal(err)
			}
			page, err := s.ChatChanges(ctx, bob.ID, domain.ChatTarget{Kind: domain.ChatDirect, ID: alice.ID}, 0)
			if err != nil || len(page.Messages) != 1 || page.Messages[0].ID != m.ID || !page.Messages[0].Deleted || page.Messages[0].Text != "" || len(page.Messages[0].AttachmentIDs) != 0 {
				t.Fatal("last file did not become tombstone", page, err)
			}
			if _, err = protocol.EncodeChatPage(page); err != nil {
				t.Fatal("invalid cleanup tombstone", err)
			}
		})
	}
}

func TestAdminFileDeletionRollsBackWhenChangeCannotBePublished(t *testing.T) {
	s, alice, bob, _ := chatTestStore(t)
	ctx := context.Background()
	target := domain.ChatTarget{Kind: domain.ChatDirect, ID: bob.ID}
	u := readyFile(t, s, alice.ID, target, 1)
	if _, err := s.SaveChatFiles(ctx, alice.ID, "alice", target, [16]byte{1}, "caption", []int64{u.AttachmentID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`CREATE TRIGGER fail_file_change BEFORE INSERT ON chat_changes BEGIN SELECT RAISE(ABORT,'injected failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteFile(ctx, "local-console", u.AttachmentID); err == nil {
		t.Fatal("ignored change publication failure")
	}
	file, err := s.FileAttachment(ctx, u.AttachmentID)
	if err != nil || file.Deleted {
		t.Fatal("revocation committed without change", err)
	}
	upload, err := s.FileUpload(ctx, u.ID)
	if err != nil || upload.State != "attached" {
		t.Fatal("upload changed after rollback", err)
	}
	page, err := s.ChatHistory(ctx, alice.ID, target, 0, false)
	if err != nil || len(page.Messages) != 1 || len(page.Messages[0].AttachmentIDs) != 1 || page.Messages[0].Revision != 0 {
		t.Fatal("history changed after rollback", page, err)
	}
}
