package filetransfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
	"uniclog.io/sonoryx/apps/server/internal/persist"
	"uniclog.io/sonoryx/internal/domain"
)

func (s *Server) enqueueVerification(ctx context.Context, id string) (persist.FileUpload, error) {
	s.verifyMu.Lock()
	defer s.verifyMu.Unlock()
	u, err := s.store.FileUpload(ctx, id)
	if err != nil {
		return u, err
	}
	if u.State == "ready" || u.State == "attached" {
		return u, nil
	}
	if (u.State != "uploading" && u.State != "verifying") || u.Offset != u.Size {
		return u, persist.ErrFileConflict
	}
	if _, ok := s.verifying[id]; ok {
		return u, nil
	}
	if len(s.verifyQueue) == cap(s.verifyQueue) {
		return u, errVerificationBusy
	}
	if u.State == "uploading" && u.Offset == u.Size {
		u, err = s.store.BeginFileVerification(ctx, id)
	}
	if err != nil {
		return u, err
	}
	if u.State != "verifying" {
		return u, persist.ErrFileConflict
	}
	s.verifying[id] = true
	s.verifyQueue <- id
	return u, nil
}

var errVerificationBusy = errors.New("verification queue full")

func (s *Server) verificationAllowed(ctx context.Context, u persist.FileUpload) bool {
	current, err := s.store.FileUpload(ctx, u.ID)
	if err != nil || current.State != "verifying" {
		return false
	}
	a, err := s.store.Account(ctx, u.UserID)
	if err != nil || a.Banned || !s.settings().UploadsEnabled {
		return false
	}
	up, _, err := s.store.FileRights(ctx, u.UserID)
	if err != nil || !up {
		return false
	}
	if u.Kind == domain.ChatChannel {
		for _, c := range s.hub.ListChannels() {
			if int64(c.ID) == u.TargetID {
				return a.JoinLevel >= c.MinJoinLevel
			}
		}
		return false
	}
	peer, err := s.store.Account(ctx, u.TargetID)
	return err == nil && !peer.Banned
}

func (s *Server) verificationWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-s.verifyQueue:
			if ctx.Err() != nil {
				return
			}
			u, err := s.store.FileUpload(ctx, id)
			if err == nil && u.State == "verifying" {
				jobCtx, cancel := context.WithDeadline(ctx, time.UnixMilli(u.Updated).Add(30*time.Minute))
				done := make(chan struct{})
				go func() {
					defer close(done)
					ticker := time.NewTicker(500 * time.Millisecond)
					defer ticker.Stop()
					for {
						select {
						case <-jobCtx.Done():
							return
						case <-ticker.C:
							if !s.verificationAllowed(jobCtx, u) {
								cancel()
								return
							}
						}
					}
				}()
				if !s.verificationAllowed(jobCtx, u) {
					err = errors.New("verification access revoked")
				} else {
					err = s.publishVerified(jobCtx, u)
				}
				cancel()
				<-done
				if err != nil && ctx.Err() == nil {
					log.Printf("file verification %s failed: %v", id, err)
					failureCtx, stop := context.WithTimeout(ctx, 5*time.Second)
					if e := s.store.FailFileVerification(failureCtx, id, "verification_failed"); e != nil {
						log.Printf("file verification checkpoint %s: %v", id, e)
					}
					stop()
				}
			}
			s.verifyMu.Lock()
			delete(s.verifying, id)
			s.verifyMu.Unlock()
		}
	}
}

func (s *Server) publishVerified(ctx context.Context, u persist.FileUpload) error {
	temp, blob := s.uploadPath(u.ID), s.blobPath(u.SHA256)
	if _, err := os.Stat(temp); errors.Is(err, os.ErrNotExist) {
		if err = verifyBlob(ctx, blob, u.Size, u.SHA256); err == nil {
			return s.finishVerified(ctx, u)
		}
	}
	f, err := os.OpenFile(temp, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	stat, err := f.Stat()
	if err != nil || stat.Size() != u.Size {
		f.Close()
		return errors.New("verification size mismatch")
	}
	hash := sha256.New()
	_, err = io.Copy(hash, &contextReader{ctx, f})
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if hex.EncodeToString(hash.Sum(nil)) != u.SHA256 {
		return errors.New("verification checksum mismatch")
	}
	// The verifying row pins both tmp and blob during GC. Hashing and disk
	// publication do not hold policyGate; only the final authorization/SQL do.
	if err = publishFile(temp, blob); err != nil {
		if err = verifyBlob(ctx, blob, u.Size, u.SHA256); err != nil {
			return err
		}
		_ = os.Remove(temp)
	}
	if err = syncDirectory(filepath.Dir(blob)); err != nil {
		return err
	}
	return s.finishVerified(ctx, u)
}

func (s *Server) finishVerified(ctx context.Context, u persist.FileUpload) error {
	s.files.RLock()
	defer s.files.RUnlock()
	s.policyGate.Lock()
	defer s.policyGate.Unlock()
	if !s.verificationAllowed(ctx, u) {
		return errors.New("verification access revoked")
	}
	_, err := s.store.CompleteFile(ctx, u.ID)
	return err
}
