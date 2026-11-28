package persist

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"uniclog.io/sonoryx/internal/domain"
)

func TestFileCleanupKeepsRecentResultsAndReferencedTombstones(t *testing.T) {
	store, a, b, _ := chatTestStore(t)
	ctx := context.Background()
	target := domain.ChatTarget{Kind: domain.ChatDirect, ID: b.ID}
	expired := readyFile(t, store, a.ID, target, 1)
	recent := readyFile(t, store, a.ID, target, 2)
	referenced := readyFile(t, store, a.ID, target, 3)
	for _, upload := range []FileUpload{expired, recent} {
		if err := store.CancelFile(ctx, upload.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.SaveChatFiles(ctx, a.ID, "alice", target, [16]byte{1}, "", []int64{referenced.AttachmentID}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteFile(ctx, "local-console", referenced.AttachmentID); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().Add(-25 * time.Hour).UnixMilli()
	if _, err := store.db.ExecContext(ctx, `UPDATE file_uploads SET terminal_at=? WHERE id IN(?,?)`, cutoff, expired.ID, referenced.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.ExpireFiles(ctx, time.Hour, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FileUpload(ctx, expired.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("expired unbound upload retained", err)
	}
	if _, err := store.FileAttachment(ctx, expired.AttachmentID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("expired unbound metadata retained", err)
	}
	if _, err := store.FileUpload(ctx, recent.ID); err != nil {
		t.Fatal("recent cancellation removed", err)
	}
	tombstone, err := store.FileAttachment(ctx, referenced.AttachmentID)
	if err != nil || !tombstone.Deleted || tombstone.MessageID == 0 {
		t.Fatal("history tombstone removed", err)
	}
}

func TestCancelledEmptyUploadsStillCountTowardsRecordQuota(t *testing.T) {
	store, a, b, _ := chatTestStore(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	_, err := store.db.ExecContext(ctx, `WITH RECURSIVE seq(n) AS(VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<4096)
 INSERT INTO file_uploads(id,request_id,user_id,kind,target_id,name,size,sha256,updated,state,terminal_at)
 SELECT printf('%032x',n),printf('request-%016x',n),?,2,?,'empty',0,?,?,'cancelled',? FROM seq`, a.ID, b.ID, strings.Repeat("a", 64), now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.CreateFileUpload(ctx, FileUpload{ID: fmt.Sprintf("%032x", 5000), RequestID: strings.Repeat("f", 16), UserID: a.ID, Kind: domain.ChatDirect, TargetID: b.ID, Name: "empty", SHA256: strings.Repeat("b", 64)}, 1, 1)
	if !errors.Is(err, ErrFileQuota) {
		t.Fatal("cancelled empty uploads bypassed record quota", err)
	}
	if _, err = store.db.ExecContext(ctx, `UPDATE file_uploads SET terminal_at=?`, time.Now().Add(-25*time.Hour).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err = store.ExpireFiles(ctx, time.Hour, 0); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err = store.db.QueryRowContext(ctx, `SELECT count(*) FROM file_uploads`).Scan(&remaining); err != nil || remaining != 4096-200 {
		t.Fatal("cleanup batch is not bounded", remaining, err)
	}
}
