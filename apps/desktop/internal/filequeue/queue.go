package filequeue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/fileapi"
	"uniclog.io/sonoryx/internal/fileops"
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
type Transfers interface {
	CheckSession(string) error
	FileCapabilities(context.Context, string) (map[string]any, error)
	FileMetadata(context.Context, string, []string) ([]fileapi.FileAttachment, error)
	UploadStatus(context.Context, string, string) (fileapi.FileUpload, error)
	StartUpload(context.Context, string, fileapi.FileUpload) (fileapi.FileUpload, error)
	WriteChunk(context.Context, string, string, int64, []byte) (fileapi.FileUpload, error)
	FinishUpload(context.Context, string, string) (fileapi.FileUpload, error)
	CancelUpload(context.Context, string, string) error
	ReadRange(context.Context, string, int64, int64, int64, int64, string, io.Writer) (int64, error)
	Retryable(error) bool
}

type FileQueue struct {
	transfers Transfers
	ctx       context.Context
	cancel    context.CancelFunc
	started   bool
	stopped   bool
	stageDir  string
	mu        sync.Mutex
	tasks     []FileTask
	path      string
	active    map[string]context.CancelFunc
	wake      chan struct{}
	done      chan struct{}
	workers   sync.WaitGroup
}

func New(transfers Transfers, profileDir string) *FileQueue {
	ctx, cancel := context.WithCancel(context.Background())
	q := &FileQueue{transfers: transfers, ctx: ctx, cancel: cancel, active: make(map[string]context.CancelFunc), wake: make(chan struct{}, 1), done: make(chan struct{})}
	if profileDir != "" {
		q.path = filepath.Join(profileDir, "file-queue.json")
		q.stageDir = filepath.Join(profileDir, "file-staging")
	}
	return q
}

// startLocked restores the queue on first use. Closing an unused queue does no disk I/O.
func (q *FileQueue) startLocked() {
	if q.started || q.stopped {
		return
	}
	q.started = true
	if q.path != "" {
		if b, err := os.ReadFile(q.path); err == nil && len(b) <= 1<<20 {
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
			q.cleanupStagedLocked(q.tasks[i])
		}
	}
	q.cleanupOrphanStages()
	go q.run(q.ctx)
}

func (q *FileQueue) cleanupOrphanStages() {
	dir := q.stageDir
	if dir == "" {
		return
	}
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
		err = fileops.CommitFile(f.Name(), q.path)
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
	q.startLocked()
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
	if err := q.transfers.CheckSession(t.Context); err != nil {
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
			t.Name = fileops.SafeName(stat.Name())
		}
		ctx, cancel := context.WithTimeout(q.ctx, 10*time.Second)
		caps, err := q.transfers.FileCapabilities(ctx, t.Context)
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
	if q.stopped {
		return t, context.Canceled
	}
	q.startLocked()
	if t.Staged {
		if err := q.checkStageLocked(t.Path); err != nil {
			return t, err
		}
		// The staging file could have changed while capabilities were fetched.
		stat, err := os.Stat(t.Path)
		if err != nil {
			return t, err
		}
		if !stat.Mode().IsRegular() || stat.Size() != t.Size {
			return t, errors.New("staged file changed before enqueue")
		}
	}
	// Terminal entries are not shown in chat and must not fill the queue.
	kept := make([]FileTask, 0, len(q.tasks)+1)
	var retired []FileTask
	for _, old := range q.tasks {
		_, active := q.active[old.ID]
		if !active && (old.State == "done" || old.State == "cancelled") {
			retired = append(retired, old)
			continue
		}
		kept = append(kept, old)
	}
	if len(kept) >= 256 {
		return t, errors.New("file queue full")
	}
	if t.Direction == "upload" {
		count := 0
		for _, old := range kept {
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
	previous := q.tasks
	q.tasks = append(kept, t)
	if err := q.saveLocked(); err != nil {
		q.tasks = previous
		return t, err
	}
	for _, old := range retired {
		q.cleanupStagedLocked(old)
	}
	q.signal()
	return t, nil
}
func (q *FileQueue) Control(expected, id, action string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.stopped {
		return context.Canceled
	}
	q.startLocked()
	for i := range q.tasks {
		t := &q.tasks[i]
		if t.ID != id || t.Context != expected {
			continue
		}
		previous := *t
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
			q.cleanupStagedLocked(*t)
			return nil
		case "pause":
			if t.State == "done" || t.State == "cancelled" {
				return nil
			}
			t.State = "paused"
		case "resume":
			if t.State == "done" || t.State == "ready" || t.State == "cancelled" {
				return nil
			}
			t.State = "queued"
			t.Error = ""
			t.Attempts = 0
			t.RetryAt = 0
		case "cancel":
			if t.State == "done" || t.State == "cancelled" {
				return nil
			}
			t.State = "cancelled"
		case "remove":
			if _, ok := q.active[id]; ok {
				return errors.New("pause transfer before removing")
			}
			if t.State != "done" && t.State != "cancelled" {
				return errors.New("cancel transfer before removing")
			}
			original := q.tasks
			q.tasks = append(q.tasks[:i:i], q.tasks[i+1:]...)
			if err := q.saveLocked(); err != nil {
				q.tasks = original
				return err
			}
			q.cleanupStagedLocked(previous)
			return nil
		default:
			return errors.New("invalid transfer action")
		}
		if err := q.saveLocked(); err != nil {
			*t = previous
			return err
		}
		if cancel := q.active[id]; cancel != nil {
			cancel()
		} else if action == "cancel" {
			snapshot := *t
			q.workers.Add(1)
			go func() {
				defer q.workers.Done()
				q.cancelTask(snapshot)
			}()
		}
		q.signal()
		return nil
	}
	return errors.New("transfer not found")
}
func (q *FileQueue) update(ctx context.Context, t FileTask) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
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
		if q.stopped {
			q.mu.Unlock()
			return
		}
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
			if err := q.transfers.CheckSession(t.Context); err != nil {
				continue
			}
			runCtx, cancel := context.WithCancel(ctx)
			q.active[t.ID] = cancel
			q.tasks[i].State = "running"
			t.State = "running"
			q.workers.Add(1)
			go q.execute(runCtx, cancel, t)
		}
		q.mu.Unlock()
	}
}
func (q *FileQueue) execute(ctx context.Context, cancel context.CancelFunc, t FileTask) {
	defer q.workers.Done()
	defer cancel()
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
		} else if old.State == "queued" && ctx.Err() != nil {
			t.State, t.Error = old.State, old.Error
			t.Attempts, t.RetryAt = old.Attempts, old.RetryAt
		} else if err != nil {
			t.State = "error"
			t.Error = err.Error()
			if e := q.transfers.CheckSession(t.Context); e != nil || q.ctx.Err() != nil {
				t.State = "queued"
			} else if (q.transfers.Retryable(err) || errors.Is(err, context.Canceled)) && t.Attempts < 5 {
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
	if t.State == "done" && saveErr == nil {
		q.cleanupStagedLocked(t)
	}
	q.mu.Unlock()
	if saveErr != nil {
		log.Printf("file queue checkpoint failed: %v", saveErr)
	}
	if t.State == "cancelled" {
		q.cancelTask(t)
	}
	q.signal()
}
func (q *FileQueue) Close(ctx context.Context) error {
	q.mu.Lock()
	if !q.stopped {
		q.stopped = true
		q.cancel()
	}
	started := q.started
	q.mu.Unlock()
	if !started {
		return nil
	}
	select {
	case <-q.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (q *FileQueue) cancelTask(t FileTask) {
	defer func() {
		q.mu.Lock()
		defer q.mu.Unlock()
		q.cleanupStagedLocked(t)
	}()
	if t.Direction == "download" {
		_ = os.Remove(t.Path + ".sonoryx-" + t.ID + ".part")
		return
	}
	if t.UploadID != "" {
		ctx, cancel := context.WithTimeout(q.ctx, 5*time.Second)
		defer cancel()
		_ = q.transfers.CancelUpload(ctx, t.Context, t.UploadID)
	}
}
func (q *FileQueue) cleanupStagedLocked(t FileTask) {
	if !t.Staged || q.stageDir == "" || filepath.Dir(t.Path) != q.stageDir {
		return
	}
	// Older profiles may have multiple tasks sharing a staged file.
	for _, other := range q.tasks {
		if other.ID != t.ID && other.Staged && other.Path == t.Path && other.State != "done" && other.State != "cancelled" {
			return
		}
	}
	_ = os.Remove(t.Path)
}
func (q *FileQueue) upload(ctx context.Context, t *FileTask) error {
	// A completed server upload no longer needs its local source file.
	if t.UploadID != "" {
		status, err := q.transfers.UploadStatus(ctx, t.Context, t.UploadID)
		if err != nil {
			return err
		}
		if status.State == "ready" || status.State == "attached" || status.State == "verifying" {
			return q.waitVerified(ctx, t, status)
		}
		if status.State != "uploading" {
			return errors.New("upload expired or cancelled; select file again")
		}
	}
	f, err := os.Open(t.Path)
	if err != nil {
		return err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	if stat.Size() != t.Size {
		return errors.New("source file size changed")
	}
	h := sha256.New()
	if _, err = io.Copy(h, &cancelReader{ctx, f}); err != nil {
		return err
	}
	hash := hex.EncodeToString(h.Sum(nil))
	if t.SHA256 != "" && t.SHA256 != hash {
		return errors.New("source file changed")
	}
	t.SHA256 = hash
	var upload fileapi.FileUpload
	if t.UploadID == "" {
		upload, err = q.transfers.StartUpload(ctx, t.Context, fileapi.FileUpload{RequestID: t.ID, Kind: t.Kind, TargetID: t.TargetID, Name: t.Name, Size: t.Size, SHA256: t.SHA256})
	} else {
		upload, err = q.transfers.UploadStatus(ctx, t.Context, t.UploadID)
	}
	if err != nil {
		return err
	}
	t.UploadID = upload.ID
	t.Offset = upload.Offset
	if err = q.update(ctx, *t); err != nil {
		return err
	}
	if upload.State == "ready" || upload.State == "attached" || upload.State == "verifying" {
		return q.waitVerified(ctx, t, upload)
	}
	if upload.State != "uploading" {
		return errors.New("upload expired or cancelled; select file again")
	}
	if _, err = f.Seek(t.Offset, 0); err != nil {
		return err
	}
	buffer := make([]byte, fileapi.ChunkSize)
	for t.Offset < t.Size {
		n, e := io.ReadFull(f, buffer[:min(int64(len(buffer)), t.Size-t.Offset)])
		if e != nil {
			return e
		}
		upload, e = q.transfers.WriteChunk(ctx, t.Context, t.UploadID, t.Offset, buffer[:n])
		if e != nil {
			return e
		}
		if upload.Offset != t.Offset+int64(n) {
			return errors.New("invalid upload checkpoint")
		}
		t.Offset = upload.Offset
		if e = q.update(ctx, *t); e != nil {
			return e
		}
	}
	upload, err = q.transfers.FinishUpload(ctx, t.Context, t.UploadID)
	if err != nil {
		return err
	}
	return q.waitVerified(ctx, t, upload)
}

func (q *FileQueue) waitVerified(ctx context.Context, t *FileTask, upload fileapi.FileUpload) error {
	deadline := time.NewTimer(31 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if upload.ID != t.UploadID || upload.Size != t.Size || upload.SHA256 != t.SHA256 {
			return errors.New("upload metadata changed")
		}
		switch upload.State {
		case "ready", "attached":
			if upload.AttachmentID <= 0 {
				return errors.New("invalid verified attachment")
			}
			t.Offset = upload.Size
			t.AttachmentID = upload.AttachmentID
			t.State = "ready"
			if upload.State == "attached" {
				t.State = "done"
			}
			return nil
		case "verifying":
			if t.State != "verifying" {
				t.State = "verifying"
				if err := q.update(ctx, *t); err != nil {
					return err
				}
			}
		default:
			if upload.Error != "" {
				return errors.New(upload.Error)
			}
			return errors.New("upload verification failed or cancelled")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return errors.New("upload verification timed out")
		case <-ticker.C:
		}
		var err error
		upload, err = q.transfers.UploadStatus(ctx, t.Context, t.UploadID)
		if err != nil {
			return err
		}
	}
}
func (q *FileQueue) download(ctx context.Context, t *FileTask) error {
	metadata, err := q.transfers.FileMetadata(ctx, t.Context, []string{strconv.FormatInt(t.AttachmentID, 10)})
	if err != nil {
		return err
	}
	if len(metadata) != 1 || metadata[0].Deleted {
		return errors.New("attachment deleted")
	}
	att := metadata[0]
	if t.SHA256 != "" && t.SHA256 != att.SHA256 {
		return errors.New("attachment content changed")
	}
	t.SHA256 = att.SHA256
	t.Size = att.Size
	path := t.Path + ".sonoryx-" + t.ID + ".part"
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	offset := min(stat.Size(), t.Offset)
	if offset > t.Size {
		return errors.New("invalid partial file")
	}
	if err = f.Truncate(offset); err != nil {
		return err
	}
	if _, err = f.Seek(offset, 0); err != nil {
		return err
	}
	for offset < t.Size {
		end := min(offset+fileapi.ChunkSize, t.Size) - 1
		n, e := q.transfers.ReadRange(ctx, t.Context, t.AttachmentID, offset, end, t.Size, t.SHA256, f)
		if e != nil {
			_ = f.Truncate(offset)
			return e
		}
		if e = f.Sync(); e != nil {
			return e
		}
		offset += n
		t.Offset = offset
		if e = q.update(ctx, *t); e != nil {
			return e
		}
	}
	if _, err = f.Seek(0, 0); err != nil {
		return err
	}
	h := sha256.New()
	if _, err = io.Copy(h, &cancelReader{ctx, f}); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != t.SHA256 {
		_ = f.Truncate(0)
		t.Offset = 0
		return errors.New("download checksum mismatch")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return q.commitDownload(ctx, t, path)
}

// Serialize publishing the final file with pause and cancel in Control.
func (q *FileQueue) commitDownload(ctx context.Context, t *FileTask, partial string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	for i := range q.tasks {
		if q.tasks[i].ID != t.ID {
			continue
		}
		if err := fileops.CommitFile(partial, t.Path); err != nil {
			return err
		}
		t.State = "done"
		q.tasks[i] = *t
		return nil
	}
	return context.Canceled
}

type cancelReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *cancelReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(b)
}
