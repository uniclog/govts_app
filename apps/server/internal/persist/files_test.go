package persist

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"uniclog.io/sonoryx/internal/domain"
)

func readyFile(t *testing.T, s *Store, user int64, target domain.ChatTarget, seed int) FileUpload {
	t.Helper()
	ctx := context.Background()
	u, err := s.CreateFileUpload(ctx, FileUpload{ID: fmt.Sprintf("%032x", seed), RequestID: fmt.Sprintf("request-%016x", seed), UserID: user, Kind: target.Kind, TargetID: target.ID, Name: "file.txt", Size: 3, SHA256: strings.Repeat("a", 64)}, 1<<30, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CheckpointFile(ctx, u.ID, 0, 3); err != nil {
		t.Fatal(err)
	}
	u, err = s.CompleteFile(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
func TestFilesAttachAtomicIdempotentAndIsolated(t *testing.T) {
	s, alice, bob, eve := chatTestStore(t)
	ctx := context.Background()
	target := domain.ChatTarget{Kind: domain.ChatDirect, ID: bob.ID}
	u := readyFile(t, s, alice.ID, target, 1)
	if _, err := s.SaveChatFiles(ctx, eve.ID, "eve", target, [16]byte{1}, "", []int64{u.AttachmentID}); err == nil {
		t.Fatal("attached someone else's upload")
	}
	if _, err := s.SaveChatFiles(ctx, alice.ID, "alice", target, [16]byte{1}, "", []int64{u.AttachmentID, 9999}); err == nil {
		t.Fatal("accepted invalid attachment")
	}
	state, err := s.FileUpload(ctx, u.ID)
	if err != nil || state.State != "ready" {
		t.Fatalf("rollback: %+v %v", state, err)
	}
	m, err := s.SaveChatFiles(ctx, alice.ID, "alice", target, [16]byte{1}, "", []int64{u.AttachmentID})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.SaveChatFiles(ctx, alice.ID, "alice", target, [16]byte{1}, "", []int64{u.AttachmentID})
	if err != nil || retry.ID != m.ID {
		t.Fatalf("retry: %+v %v", retry, err)
	}
	if err = s.CancelFile(ctx, u.ID); err == nil {
		t.Fatal("cancelled attached file")
	}
	if _, err = s.SaveChatFiles(ctx, alice.ID, "alice", target, [16]byte{2}, "", []int64{u.AttachmentID}); err == nil {
		t.Fatal("reused attachment")
	}
	page, err := s.ChatHistory(ctx, bob.ID, domain.ChatTarget{Kind: domain.ChatDirect, ID: alice.ID}, 0, false)
	if err != nil || len(page.Messages) != 1 || len(page.Messages[0].AttachmentIDs) != 1 {
		t.Fatalf("history %+v %v", page, err)
	}
}
func TestFileReservationAndCheckpointBoundaries(t *testing.T) {
	s, a, b, _ := chatTestStore(t)
	ctx := context.Background()
	u := FileUpload{ID: strings.Repeat("1", 32), RequestID: strings.Repeat("1", 16), UserID: a.ID, Kind: domain.ChatDirect, TargetID: b.ID, Name: "f", Size: 10, SHA256: strings.Repeat("a", 64)}
	first, err := s.CreateFileUpload(ctx, u, 10, 10)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.CreateFileUpload(ctx, u, 10, 10)
	if err != nil || first.ID != again.ID {
		t.Fatalf("retry %+v %v", again, err)
	}
	u.RequestID = strings.Repeat("2", 16)
	u.ID = strings.Repeat("2", 32)
	if _, err = s.CreateFileUpload(ctx, u, 10, 10); err == nil {
		t.Fatal("overbooked quota")
	}
	if err = s.CheckpointFile(ctx, first.ID, 1, 2); err == nil {
		t.Fatal("accepted stale offset")
	}
	if err = s.CheckpointFile(ctx, first.ID, 0, 11); err == nil {
		t.Fatal("accepted oversize offset")
	}
	if _, err = s.CompleteFile(ctx, first.ID); err == nil {
		t.Fatal("completed incomplete upload")
	}
	if err = s.CancelFile(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateFileUpload(ctx, u, 10, 10); err != nil {
		t.Fatalf("quota not released: %v", err)
	}
}
func TestFileRightsAndRetentionKeepTombstone(t *testing.T) {
	s, a, b, _ := chatTestStore(t)
	ctx := context.Background()
	target := domain.ChatTarget{Kind: domain.ChatDirect, ID: b.ID}
	u := readyFile(t, s, a.ID, target, 1)
	if err := s.SetFileRights(ctx, "local-console", a.ID, false, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveChatFiles(ctx, a.ID, "alice", target, [16]byte{1}, "", []int64{u.AttachmentID}); err == nil {
		t.Fatal("rights bypass")
	}
	if err := s.SetFileRights(ctx, "local-console", a.ID, true, true); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveChatFiles(ctx, a.ID, "alice", target, [16]byte{1}, "", []int64{u.AttachmentID}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE file_attachments SET created=1`); err != nil {
		t.Fatal(err)
	}
	if err := s.ExpireFiles(ctx, time.Hour, time.Second); err != nil {
		t.Fatal(err)
	}
	att, err := s.FileAttachment(ctx, u.AttachmentID)
	if err != nil || !att.Deleted || att.MessageID == 0 {
		t.Fatalf("tombstone %+v %v", att, err)
	}
}
