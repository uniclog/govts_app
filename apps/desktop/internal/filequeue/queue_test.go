package filequeue

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"uniclog.io/sonoryx/internal/fileapi"
)

func TestVerifiedUploadRejectsInvalidMetadata(t *testing.T) {
	for _, state := range []string{"ready", "attached"} {
		for _, field := range []string{"id", "size", "hash", "attachment"} {
			t.Run(state+"/"+field, func(t *testing.T) {
				task := FileTask{UploadID: "upload", Size: 10, SHA256: "hash", State: "running"}
				upload := fileapi.FileUpload{ID: "upload", Size: 10, SHA256: "hash", State: state, AttachmentID: 1}
				switch field {
				case "id":
					upload.ID = "other"
				case "size":
					upload.Size++
				case "hash":
					upload.SHA256 = "other"
				case "attachment":
					upload.AttachmentID = 0
				}
				if err := (&FileQueue{}).waitVerified(context.Background(), &task, upload); err == nil {
					t.Fatal("accepted invalid completed upload")
				}
				if task.State != "running" || task.AttachmentID != 0 {
					t.Fatal("invalid response changed the task")
				}
			})
		}
	}
}

func TestAttachmentCompletionRetriesFailedLocalCheckpoint(t *testing.T) {
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("block"), 0600); err != nil {
		t.Fatal(err)
	}
	q := New(nil, "")
	t.Cleanup(func() { _ = q.Close(context.Background()) })
	q.List("ctx")
	q.mu.Lock()
	q.path = filepath.Join(blocked, "queue.json")
	q.tasks = []FileTask{{ID: "one", Context: "ctx", State: "ready"}}
	q.mu.Unlock()
	if err := q.Control("ctx", "one", "attached"); err == nil {
		t.Fatal("accepted failed checkpoint")
	}
	if q.List("ctx")[0].State != "ready" {
		t.Fatal("failed save prevented retry")
	}
	path := filepath.Join(t.TempDir(), "queue.json")
	q.mu.Lock()
	q.path = path
	q.mu.Unlock()
	if err := q.Control("ctx", "one", "attached"); err != nil {
		t.Fatal(err)
	}
	if err := q.Control("ctx", "one", "attached"); err != nil {
		t.Fatal("completion not idempotent", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tasks []FileTask
	if err = json.Unmarshal(data, &tasks); err != nil || len(tasks) != 1 || tasks[0].State != "done" {
		t.Fatal("completion was not persisted", err)
	}
}
