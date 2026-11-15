package clientapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"uniclog.io/sonoryx/internal/atomicfile"
	"uniclog.io/sonoryx/internal/filesignal"
)

func (q *FileQueue) upload(ctx context.Context, t *FileTask) error {
	// A completed server upload no longer needs its local source file.
	if t.UploadID != "" {
		var status filesignal.FileUpload
		if _, err := q.app.fileJSON(ctx, t.Context, "GET", "/files/v1/uploads/"+t.UploadID, nil, &status); err != nil {
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
	var upload filesignal.FileUpload
	if t.UploadID == "" {
		_, err = q.app.fileJSON(ctx, t.Context, "POST", "/files/v1/uploads", filesignal.FileUpload{RequestID: t.ID, Kind: t.Kind, TargetID: t.TargetID, Name: t.Name, Size: t.Size, SHA256: t.SHA256}, &upload)
	} else {
		_, err = q.app.fileJSON(ctx, t.Context, "GET", "/files/v1/uploads/"+t.UploadID, nil, &upload)
	}
	if err != nil {
		return err
	}
	t.UploadID = upload.ID
	t.Offset = upload.Offset
	if err = q.update(*t); err != nil {
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
	buffer := make([]byte, filesignal.ChunkSize)
	for t.Offset < t.Size {
		n, e := io.ReadFull(f, buffer[:min(int64(len(buffer)), t.Size-t.Offset)])
		if e != nil {
			return e
		}
		chunk := buffer[:n]
		sum := sha256.Sum256(chunk)
		response, close, e := q.app.fileRequest(ctx, t.Context, "PATCH", "/files/v1/uploads/"+t.UploadID+"/content", bytes.NewReader(chunk), map[string]string{"Upload-Offset": strconv.FormatInt(t.Offset, 10), "Upload-Checksum": hex.EncodeToString(sum[:])})
		if e != nil {
			return e
		}
		e = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&upload)
		close()
		if e != nil {
			return e
		}
		if upload.Offset != t.Offset+int64(n) {
			return errors.New("invalid upload checkpoint")
		}
		t.Offset = upload.Offset
		if e = q.update(*t); e != nil {
			return e
		}
	}
	_, err = q.app.fileJSON(ctx, t.Context, "POST", "/files/v1/uploads/"+t.UploadID+"/complete", nil, &upload)
	if err != nil {
		return err
	}
	return q.waitVerified(ctx, t, upload)
}

func (q *FileQueue) waitVerified(ctx context.Context, t *FileTask, upload filesignal.FileUpload) error {
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
				q.cleanupStaged(*t)
			}
			return nil
		case "verifying":
			if t.State != "verifying" {
				t.State = "verifying"
				if err := q.update(*t); err != nil {
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
		if _, err := q.app.fileJSON(ctx, t.Context, "GET", "/files/v1/uploads/"+t.UploadID, nil, &upload); err != nil {
			return err
		}
	}
}

func (q *FileQueue) download(ctx context.Context, t *FileTask) error {
	metadata, err := q.app.FileMetadata(ctx, t.Context, []string{strconv.FormatInt(t.AttachmentID, 10)})
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
		end := min(offset+filesignal.ChunkSize, t.Size) - 1
		response, close, e := q.app.fileRequest(ctx, t.Context, "GET", "/files/v1/attachments/"+strconv.FormatInt(t.AttachmentID, 10)+"/content", nil, map[string]string{"Range": fmt.Sprintf("bytes=%d-%d", offset, end), "If-Range": "\"" + t.SHA256 + "\""})
		if e != nil {
			return e
		}
		if response.StatusCode != http.StatusPartialContent || response.Header.Get("ETag") != "\""+t.SHA256+"\"" || response.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", offset, end, t.Size) {
			close()
			return errors.New("invalid download range")
		}
		n, e := io.CopyN(f, &cancelReader{ctx, response.Body}, end-offset+1)
		close()
		if e != nil {
			_ = f.Truncate(offset)
			return e
		}
		if e = f.Sync(); e != nil {
			return e
		}
		offset += n
		t.Offset = offset
		if e = q.update(*t); e != nil {
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
	if err = atomicfile.Commit(path, t.Path); err != nil {
		return err
	}
	t.State = "done"
	return nil
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
