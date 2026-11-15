package clientapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"uniclog.io/sonoryx/internal/atomicfile"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/filesignal"
)

type FileTask struct {
	ID           string          `json:"id"`
	Context      string          `json:"context"`
	Direction    string          `json:"direction"`
	Kind         domain.ChatKind `json:"kind"`
	TargetID     int64           `json:"targetId,string"`
	Name         string          `json:"name"`
	Path         string          `json:"path"`
	Size         int64           `json:"size"`
	SHA256       string          `json:"sha256"`
	Offset       int64           `json:"offset"`
	UploadID     string          `json:"uploadId"`
	AttachmentID int64           `json:"attachmentId,string"`
	State        string          `json:"state"`
	Error        string          `json:"error"`
	Attempts     int             `json:"attempts"`
	RetryAt      int64           `json:"retryAt"`
	Staged       bool            `json:"staged"`
}

type FileQueue struct {
	app     *App
	mu      sync.Mutex
	tasks   []FileTask
	path    string
	active  map[string]context.CancelFunc
	wake    chan struct{}
	done    chan struct{}
	workers sync.WaitGroup
}

func (a *App) Files() *FileQueue {
	a.fileOnce.Do(func() {
		dir, err := os.UserConfigDir()
		q := &FileQueue{app: a, active: map[string]context.CancelFunc{}, wake: make(chan struct{}, 1), done: make(chan struct{})}
		if err == nil {
			q.path = filepath.Join(dir, "Sonoryx", "file-queue.json")
			if b, e := os.ReadFile(q.path); e == nil && len(b) <= 1<<20 {
				_ = json.Unmarshal(b, &q.tasks)
			}
		}
		if len(q.tasks) > 256 {
			q.tasks = q.tasks[:256]
		}
		for i := range q.tasks {
			if q.tasks[i].State == "running" || q.tasks[i].State == "ready" || q.tasks[i].State == "verifying" {
				q.tasks[i].State = "queued"
			}
			if q.tasks[i].State == "done" || q.tasks[i].State == "cancelled" {
				q.cleanupStaged(q.tasks[i])
			}
		}
		q.cleanupOrphanStages()
		a.fileQueue = q
		go q.run(a.lifetimeCtx)
	})
	return a.fileQueue
}

func (q *FileQueue) cleanupOrphanStages() {
	dir, err := os.UserConfigDir()
	if err != nil {
		return
	}
	dir = filepath.Join(dir, "Sonoryx", "file-staging")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	used := map[string]bool{}
	for _, t := range q.tasks {
		if t.Staged && t.State != "done" && t.State != "cancelled" {
			used[t.Path] = true
		}
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		if !used[path] && entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), "staged-") {
			if stat, err := entry.Info(); err == nil && time.Since(stat.ModTime()) > 24*time.Hour {
				_ = os.Remove(path)
			}
		}
	}
}

func (q *FileQueue) saveLocked() error {
	if q.path == "" {
		return errors.New("file queue storage unavailable")
	}
	if err := os.MkdirAll(filepath.Dir(q.path), 0700); err != nil {
		return err
	}
	b, err := json.Marshal(q.tasks)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(q.path), ".file-queue-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_ = f.Chmod(0600)
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = atomicfile.Commit(f.Name(), q.path)
	}
	return err
}

func (q *FileQueue) signal() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *FileQueue) List(expected string) []FileTask {
	q.mu.Lock()
	defer q.mu.Unlock()
	list := []FileTask{}
	for _, t := range q.tasks {
		if t.Context == expected {
			list = append(list, t)
		}
	}
	return list
}

func (q *FileQueue) Add(t FileTask) (FileTask, error) {
	if t.Direction != "upload" && t.Direction != "download" {
		return t, errors.New("invalid transfer direction")
	}
	if _, err := q.app.fileConnection(t.Context); err != nil {
		return t, err
	}
	if t.Direction == "upload" {
		f, err := os.Open(t.Path)
		if err != nil {
			return t, err
		}
		defer f.Close()
		stat, err := f.Stat()
		if err != nil || !stat.Mode().IsRegular() {
			return t, errors.New("source must be a regular file")
		}
		t.Size = stat.Size()
		if t.Name == "" {
			t.Name = filesignal.SafeName(stat.Name())
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		caps, err := q.app.FileCapabilities(ctx, t.Context)
		cancel()
		if err != nil {
			return t, err
		}
		maxSize, ok := caps["maxFileBytes"].(float64)
		if !ok || t.Size > int64(maxSize) {
			return t, errors.New("file exceeds server size limit")
		}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	// Terminal entries are not shown in chat and must not fill the queue.
	kept := q.tasks[:0]
	for _, old := range q.tasks {
		_, active := q.active[old.ID]
		if !active && (old.State == "done" || old.State == "cancelled") {
			q.cleanupStaged(old)
			continue
		}
		kept = append(kept, old)
	}
	clear(q.tasks[len(kept):])
	q.tasks = kept
	if len(q.tasks) >= 256 {
		return t, errors.New("file queue full")
	}
	if t.Direction == "upload" {
		count := 0
		for _, old := range q.tasks {
			if old.Context == t.Context && old.Direction == "upload" && old.Kind == t.Kind && old.TargetID == t.TargetID && old.State != "done" && old.State != "cancelled" {
				count++
			}
		}
		if count >= domain.MaxChatAttachments {
			return t, errors.New("maximum four attachments per draft")
		}
	}
	t.ID = fmt.Sprintf("%x", time.Now().UnixNano())
	t.State = "queued"
	q.tasks = append(q.tasks, t)
	if err := q.saveLocked(); err != nil {
		q.tasks = q.tasks[:len(q.tasks)-1]
		return t, err
	}
	q.signal()
	return t, nil
}

func (q *FileQueue) Control(expected, id, action string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.tasks {
		t := &q.tasks[i]
		if t.ID != id || t.Context != expected {
			continue
		}
		switch action {
		case "attached":
			if t.State == "done" {
				return nil
			}
			if t.State != "ready" {
				return errors.New("transfer not ready")
			}
			t.State = "done"
			if err := q.saveLocked(); err != nil {
				t.State = "ready"
				return err
			}
			q.cleanupStaged(*t)
			return nil
		case "pause":
			t.State = "paused"
		case "resume":
			if t.State == "done" || t.State == "ready" {
				return nil
			}
			t.State = "queued"
			t.Error = ""
			t.Attempts = 0
			t.RetryAt = 0
		case "cancel":
			t.State = "cancelled"
		case "remove":
			if _, ok := q.active[id]; ok {
				return errors.New("pause transfer before removing")
			}
			if t.State != "done" && t.State != "cancelled" {
				return errors.New("cancel transfer before removing")
			}
			q.cleanupStaged(*t)
			q.tasks = append(q.tasks[:i], q.tasks[i+1:]...)
			return q.saveLocked()
		default:
			return errors.New("invalid transfer action")
		}
		if cancel := q.active[id]; cancel != nil {
			cancel()
		} else if action == "cancel" {
			snapshot := *t
			go q.cancelTask(snapshot)
		}
		if err := q.saveLocked(); err != nil {
			return err
		}
		q.signal()
		return nil
	}
	return errors.New("transfer not found")
}

func (q *FileQueue) update(t FileTask) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for i := range q.tasks {
		if q.tasks[i].ID == t.ID {
			if q.tasks[i].State == "paused" || q.tasks[i].State == "cancelled" {
				return context.Canceled
			}
			q.tasks[i] = t
			return q.saveLocked()
		}
	}
	return context.Canceled
}

func (q *FileQueue) run(ctx context.Context) {
	defer close(q.done)
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	defer func() {
		q.mu.Lock()
		for _, cancel := range q.active {
			cancel()
		}
		q.mu.Unlock()
		q.workers.Wait()
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-q.wake:
		}
		q.mu.Lock()
		for i := range q.tasks {
			t := q.tasks[i]
			if len(q.active) >= 2 {
				break
			}
			if t.State != "queued" {
				continue
			}
			if time.Now().UnixMilli() < t.RetryAt {
				continue
			}
			if _, ok := q.active[t.ID]; ok {
				continue
			}
			if _, err := q.app.fileConnection(t.Context); err != nil {
				continue
			}
			runCtx, cancel := context.WithCancel(ctx)
			q.active[t.ID] = cancel
			q.tasks[i].State = "running"
			t.State = "running"
			q.workers.Add(1)
			go q.execute(runCtx, t)
		}
		q.mu.Unlock()
	}
}

func (q *FileQueue) execute(ctx context.Context, t FileTask) {
	defer q.workers.Done()
	var err error
	if t.Direction == "upload" {
		err = q.upload(ctx, &t)
	} else {
		err = q.download(ctx, &t)
	}
	q.mu.Lock()
	for i := range q.tasks {
		if q.tasks[i].ID != t.ID {
			continue
		}
		old := q.tasks[i]
		if old.State == "cancelled" || old.State == "paused" {
			t.State = old.State
		} else if old.State == "queued" && errors.Is(err, context.Canceled) {
			t.State = "queued"
		} else if err != nil {
			t.State = "error"
			t.Error = err.Error()
			if _, e := q.app.fileConnection(t.Context); e != nil || q.app.lifetimeCtx.Err() != nil {
				t.State = "queued"
			} else if (retryableFileError(err) || errors.Is(err, context.Canceled)) && t.Attempts < 5 {
				t.Attempts++
				t.RetryAt = time.Now().Add(time.Duration(1<<t.Attempts) * time.Second).UnixMilli()
				t.State = "queued"
			}
		}
		q.tasks[i] = t
		break
	}
	delete(q.active, t.ID)
	saveErr := q.saveLocked()
	q.mu.Unlock()
	if saveErr != nil {
		q.app.logger.Printf("file queue checkpoint failed: %v", saveErr)
	}
	if t.State == "cancelled" {
		q.cancelTask(t)
	}
	q.signal()
}

func (q *FileQueue) Wait(ctx context.Context) error {
	select {
	case <-q.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (q *FileQueue) cancelTask(t FileTask) {
	defer q.cleanupStaged(t)
	if t.Direction == "download" {
		_ = os.Remove(t.Path + ".sonoryx-" + t.ID + ".part")
		return
	}
	if t.UploadID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = q.app.fileJSON(ctx, t.Context, "DELETE", "/files/v1/uploads/"+t.UploadID, nil, nil)
	}
}

func (q *FileQueue) cleanupStaged(t FileTask) {
	if !t.Staged {
		return
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return
	}
	if filepath.Dir(t.Path) == filepath.Join(dir, "Sonoryx", "file-staging") {
		_ = os.Remove(t.Path)
	}
}
