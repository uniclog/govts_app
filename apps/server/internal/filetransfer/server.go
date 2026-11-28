package filetransfer

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
	"uniclog.io/sonoryx/apps/server/internal/persist"
	"uniclog.io/sonoryx/apps/server/internal/voice"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/fileapi"
)

type fileSessions interface {
	AuthenticateFile(uint64, []byte) bool
	Get(uint64) (voice.Session, bool)
	ListChannels() []domain.Channel
}

type fileAdministration interface {
	DeleteAttachment(context.Context, voice.AdminActor, int64) error
	SetFileRights(context.Context, voice.AdminActor, int64, bool, bool) error
}

type fileStore interface {
	Account(context.Context, int64) (persist.Account, error)
	FilePolicy(context.Context) (string, error)
	SaveFilePolicy(context.Context, string, string) error
	FileRights(context.Context, int64) (bool, bool, error)
	FileUsage(context.Context) (map[string]int64, error)
	FileAdminList(context.Context, int64) ([]fileapi.FileAttachment, error)
	FileAttachment(context.Context, int64) (fileapi.FileAttachment, error)
	FileUpload(context.Context, string) (fileapi.FileUpload, error)
	CreateFileUpload(context.Context, fileapi.FileUpload, int64, int64) (fileapi.FileUpload, error)
	CheckpointFile(context.Context, string, int64, int64) error
	BeginFileVerification(context.Context, string) (fileapi.FileUpload, error)
	CompleteFile(context.Context, string) (fileapi.FileUpload, error)
	FailFileVerification(context.Context, string, string) error
	PendingFileVerifications(context.Context) ([]string, error)
	CancelFile(context.Context, string) error
	ExpireFiles(context.Context, time.Duration, time.Duration) error
	FileUploads(context.Context) ([]fileapi.FileUpload, error)
	FileCleanupRefs(context.Context) (map[string]bool, map[string]bool, error)
}

type Server struct {
	store                              fileStore
	hub                                fileSessions
	config                             atomic.Pointer[Config]
	policyGate                         *sync.Mutex
	administration                     fileAdministration
	verifyMu                           sync.Mutex
	verifyQueue                        chan string
	verifying                          map[string]bool
	files                              sync.RWMutex // Serializes GC against publication and readers; never a Hub mutex.
	stripes                            [64]sync.Mutex
	slots                              chan struct{}
	previewSlot                        chan struct{}
	mu                                 sync.Mutex
	users                              map[int64]*userLimit
	inbound, outbound                  *rate.Limiter
	requests, failures, received, sent atomic.Uint64
}
type userLimit struct {
	in, out *rate.Limiter
	active  int
	at      time.Time
	creates *rate.Limiter
}
type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func New(store fileStore, hub fileSessions, administration fileAdministration, c Config, gates ...*sync.Mutex) (*Server, error) {
	if store == nil || hub == nil || administration == nil {
		return nil, errors.New("file store, sessions and administration required")
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(c.Directory)
	if err != nil {
		return nil, err
	}
	c.Directory = abs
	for _, dir := range []string{"tmp", "blobs"} {
		if err = os.MkdirAll(filepath.Join(abs, dir), 0700); err != nil {
			return nil, err
		}
	}
	s := &Server{store: store, hub: hub, slots: make(chan struct{}, 256), users: make(map[int64]*userLimit), inbound: rate.NewLimiter(rate.Limit(c.ServerBytesPerSecond), 32<<10), outbound: rate.NewLimiter(rate.Limit(c.ServerBytesPerSecond), 32<<10)}
	s.config.Store(&c)
	s.policyGate = &sync.Mutex{}
	if len(gates) > 0 && gates[0] != nil {
		s.policyGate = gates[0]
	}
	s.administration = administration
	s.verifyQueue = make(chan string, 64)
	s.verifying = make(map[string]bool)
	s.previewSlot = make(chan struct{}, 1)
	if err = s.loadPolicy(context.Background()); err != nil {
		return nil, err
	}
	if err = s.recover(context.Background()); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Server) uploadPath(id string) string {
	return filepath.Join(s.settings().Directory, "tmp", id)
}
func (s *Server) blobPath(hash string) string {
	return filepath.Join(s.settings().Directory, "blobs", hash)
}
func (s *Server) stripe(id string) *sync.Mutex {
	sum := sha256.Sum256([]byte(id))
	return &s.stripes[sum[0]%64]
}
func validHash(v string) bool {
	b, e := hex.DecodeString(v)
	return e == nil && len(b) == 32 && strings.ToLower(v) == v
}

func validHex(v string) bool {
	_, err := hex.DecodeString(v)
	return err == nil && v != "" && strings.ToLower(v) == v
}
func jsonResponse(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func (s *Server) reject(w http.ResponseWriter, status int, code string) {
	s.failures.Add(1)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(APIError{code, code})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(v) != nil {
		return false
	}
	var extra any
	return errors.Is(d.Decode(&extra), io.EOF)
}

func (s *Server) actor(r *http.Request) (voice.Session, error) {
	id, err := strconv.ParseUint(r.Header.Get("X-Govts-Session"), 10, 64)
	if err != nil {
		return voice.Session{}, err
	}
	token, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if err != nil || !s.hub.AuthenticateFile(id, token) {
		return voice.Session{}, errors.New("unauthorized")
	}
	a, ok := s.hub.Get(id)
	if !ok || a.UserID <= 0 {
		return a, errors.New("unauthorized")
	}
	return a, s.check(r.Context(), a, domain.ChatTarget{})
}
func (s *Server) check(ctx context.Context, actor voice.Session, t domain.ChatTarget) error {
	a, ok := s.hub.Get(actor.ID)
	if !ok || a.UserID != actor.UserID {
		return errors.New("session ended")
	}
	account, err := s.store.Account(ctx, a.UserID)
	if err != nil || account.Banned {
		return errors.New("account unavailable")
	}
	if t.Kind == domain.ChatChannel {
		if a.ChannelID != domain.ChannelID(t.ID) {
			return errors.New("channel unavailable")
		}
		for _, c := range s.hub.ListChannels() {
			if c.ID == a.ChannelID && a.JoinLevel >= c.MinJoinLevel {
				return nil
			}
		}
		return errors.New("channel unavailable")
	}
	if t.Kind == domain.ChatDirect {
		if t.ID == a.UserID {
			return errors.New("invalid peer")
		}
		peer, err := s.store.Account(ctx, t.ID)
		if err != nil || peer.Banned {
			return errors.New("peer unavailable")
		}
	}
	return nil
}
func (s *Server) watch(w http.ResponseWriter, r *http.Request, a voice.Session, t domain.ChatTarget) (*http.Request, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	interrupt := context.AfterFunc(ctx, func() {
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(time.Now())
		_ = controller.SetWriteDeadline(time.Now())
	})
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				valid := s.check(ctx, a, t) == nil && s.allowed(ctx, a, strings.HasPrefix(r.URL.Path, "/files/v1/uploads/"))
				if strings.HasPrefix(r.URL.Path, "/files/v1/admin/") {
					account, err := s.store.Account(ctx, a.UserID)
					valid = valid && err == nil && (account.Owner || account.Permissions&persist.PermissionAdmin != 0)
				}
				if strings.HasPrefix(r.URL.Path, "/files/v1/attachments/") {
					parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/files/v1/attachments/"), "/")
					id, _ := strconv.ParseInt(parts[0], 10, 64)
					att, err := s.store.FileAttachment(ctx, id)
					valid = valid && err == nil && !att.Deleted
				}
				if !valid {
					cancel()
					return
				}
			}
		}
	}()
	return r.WithContext(ctx), func() { interrupt(); cancel() }
}
func (s *Server) acquire(a voice.Session) (*userLimit, bool) {
	select {
	case s.slots <- struct{}{}:
	default:
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.slots) > s.settings().MaxTransfers {
		<-s.slots
		return nil, false
	}
	now := time.Now()
	if len(s.users) >= 8192 {
		for id, u := range s.users {
			if u.active == 0 && now.Sub(u.at) > time.Minute {
				delete(s.users, id)
			}
		}
	}
	u := s.users[a.UserID]
	if u == nil {
		if len(s.users) >= 8192 {
			<-s.slots
			return nil, false
		}
		u = &userLimit{in: rate.NewLimiter(rate.Limit(s.settings().UserBytesPerSecond), 32<<10), out: rate.NewLimiter(rate.Limit(s.settings().UserBytesPerSecond), 32<<10), creates: rate.NewLimiter(rate.Every(6*time.Second), 4)}
		s.users[a.UserID] = u
	}
	if u.active >= 3 {
		<-s.slots
		return nil, false
	}
	u.active++
	u.at = now
	return u, true
}
func (s *Server) release(a voice.Session) {
	s.mu.Lock()
	if u := s.users[a.UserID]; u != nil {
		u.active--
		u.at = time.Now()
	}
	s.mu.Unlock()
	<-s.slots
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.requests.Add(1)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	a, err := s.actor(r)
	if err != nil {
		s.reject(w, 401, "unauthorized")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/files/v1/uploads") && (r.Method == "POST" || r.Method == "PATCH") && !s.allowed(r.Context(), a, true) {
		s.reject(w, 403, "uploads_disabled")
		return
	}
	if (strings.HasPrefix(r.URL.Path, "/files/v1/attachments") || r.URL.Path == "/files/v1/metadata") && !s.allowed(r.Context(), a, false) {
		s.reject(w, 403, "downloads_disabled")
		return
	}
	u, ok := s.acquire(a)
	if !ok {
		s.reject(w, 429, "busy")
		return
	}
	defer s.release(a)
	if strings.HasPrefix(r.URL.Path, "/files/v1/admin") {
		s.admin(w, r, a, u)
		return
	}
	switch {
	case r.Method == "GET" && r.URL.Path == "/files/v1/capabilities":
		jsonResponse(w, map[string]any{"maxFileBytes": s.settings().MaxFileBytes, "chunkBytes": ChunkSize, "maxAttachments": domain.MaxChatAttachments, "uploadsEnabled": s.allowed(r.Context(), a, true), "downloadsEnabled": s.allowed(r.Context(), a, false), "userQuotaBytes": s.settings().UserQuotaBytes, "uploadTTLSeconds": int64(s.settings().UploadTTL.Seconds())})
		return
	case r.Method == "POST" && r.URL.Path == "/files/v1/uploads":
		s.create(w, r, a, u)
		return
	case r.Method == "POST" && r.URL.Path == "/files/v1/metadata":
		var ids []string
		if !decode(w, r, &ids) || len(ids) > 64 {
			s.reject(w, 400, "invalid_request")
			return
		}
		list := make([]persist.FileAttachment, 0, len(ids))
		for _, id := range ids {
			att, err := s.attachment(r.Context(), a, id)
			if err != nil {
				continue // One unavailable attachment must not hide the rest of a batch.
			}
			list = append(list, att)
		}
		if len(ids) > 0 && len(list) == 0 {
			s.reject(w, 404, "unavailable")
			return
		}
		jsonResponse(w, list)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/files/v1/uploads/") {
		rest := strings.TrimPrefix(r.URL.Path, "/files/v1/uploads/")
		parts := strings.Split(rest, "/")
		id := parts[0]
		raw, e := hex.DecodeString(id)
		if e != nil || len(raw) != 16 {
			s.reject(w, 404, "unavailable")
			return
		}
		upload, e := s.store.FileUpload(r.Context(), id)
		if e != nil || upload.UserID != a.UserID {
			s.reject(w, 404, "unavailable")
			return
		}
		if len(parts) == 1 && r.Method == "GET" {
			jsonResponse(w, upload)
			return
		}
		if len(parts) == 1 && r.Method == "DELETE" {
			if upload.State == "cancelled" || upload.State == "expired" {
				w.WriteHeader(204)
				return
			}
			if s.store.CancelFile(r.Context(), id) != nil {
				s.reject(w, 409, "state_conflict")
				return
			}
			_ = os.Remove(s.uploadPath(id))
			w.WriteHeader(204)
			return
		}
		lock := s.stripe(id)
		lock.Lock()
		defer lock.Unlock()
		s.files.RLock()
		defer s.files.RUnlock()
		upload, e = s.store.FileUpload(r.Context(), id)
		if e != nil {
			s.reject(w, 404, "unavailable")
			return
		}
		if len(parts) != 2 {
			s.reject(w, 404, "unavailable")
			return
		}
		if s.check(r.Context(), a, domain.ChatTarget{Kind: upload.Kind, ID: upload.TargetID}) != nil {
			s.reject(w, 404, "unavailable")
			return
		}
		r, cancel := s.watch(w, r, a, domain.ChatTarget{Kind: upload.Kind, ID: upload.TargetID})
		defer cancel()
		if r.Method == "PATCH" && parts[1] == "content" {
			s.chunk(w, r, upload, u)
			return
		}
		if r.Method == "POST" && parts[1] == "complete" {
			s.complete(w, r, upload)
			return
		}
	}
	if strings.HasPrefix(r.URL.Path, "/files/v1/attachments/") {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/files/v1/attachments/"), "/")
		if len(parts) != 2 || r.Method != "GET" {
			s.reject(w, 404, "unavailable")
			return
		}
		att, e := s.attachment(r.Context(), a, parts[0])
		if e != nil || att.Deleted {
			s.reject(w, 404, "unavailable")
			return
		}
		target := domain.ChatTarget{Kind: att.Kind, ID: att.TargetID}
		if att.Kind == domain.ChatDirect && att.UserID != a.UserID {
			target.ID = att.UserID
		}
		r, cancel := s.watch(w, r, a, target)
		defer cancel()
		switch parts[1] {
		case "content":
			s.download(w, r, att, u)
		case "preview":
			s.preview(w, r, att)
		default:
			s.reject(w, 404, "unavailable")
		}
		return
	}
	s.reject(w, 404, "unavailable")
}

func (s *Server) create(w http.ResponseWriter, r *http.Request, a voice.Session, u *userLimit) {
	s.files.RLock()
	defer s.files.RUnlock()
	if !u.creates.Allow() {
		s.reject(w, 429, "rate_limit")
		return
	}
	var upload persist.FileUpload
	if !decode(w, r, &upload) || len(upload.RequestID) < 16 || len(upload.RequestID) > 64 || !validHash(upload.SHA256) || upload.Size < 0 || upload.Size > s.settings().MaxFileBytes || (domain.ChatTarget{Kind: upload.Kind, ID: upload.TargetID}).Validate() != nil {
		s.reject(w, 400, "invalid_upload")
		return
	}
	if s.check(r.Context(), a, domain.ChatTarget{Kind: upload.Kind, ID: upload.TargetID}) != nil {
		s.reject(w, 404, "unavailable")
		return
	}
	free, err := freeBytes(s.settings().Directory)
	if err != nil || free < s.settings().MinFreeBytes+uint64(upload.Size) {
		s.reject(w, 507, "disk_full")
		return
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		s.reject(w, 500, "storage_error")
		return
	}
	upload.ID = hex.EncodeToString(id[:])
	upload.UserID = a.UserID
	upload.Name = SafeName(upload.Name)
	upload.Offset = 0
	upload.AttachmentID = 0
	result, err := s.store.CreateFileUpload(r.Context(), upload, s.settings().UserQuotaBytes, s.settings().ServerQuotaBytes)
	if err != nil {
		if errors.Is(err, persist.ErrFileQuota) {
			s.reject(w, 413, "quota_exceeded")
		} else if errors.Is(err, persist.ErrFileConflict) {
			s.reject(w, 409, "request_conflict")
		} else {
			s.reject(w, 500, "storage_error")
		}
		return
	}
	jsonResponse(w, result)
}

func (s *Server) chunk(w http.ResponseWriter, r *http.Request, u persist.FileUpload, limit *userLimit) {
	offset, err := strconv.ParseInt(r.Header.Get("Upload-Offset"), 10, 64)
	if err != nil || offset != u.Offset || u.State != "uploading" {
		s.reject(w, 409, "offset_conflict")
		return
	}
	hash := r.Header.Get("Upload-Checksum")
	if !validHash(hash) || r.ContentLength < 1 || r.ContentLength > ChunkSize || r.ContentLength > u.Size-u.Offset {
		s.reject(w, 400, "invalid_chunk")
		return
	}
	free, err := freeBytes(s.settings().Directory)
	if err != nil || free < s.settings().MinFreeBytes+uint64(r.ContentLength) {
		s.reject(w, 507, "disk_full")
		return
	}
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(5 * time.Minute))
	body, err := io.ReadAll(io.LimitReader(&pacedReader{ctx: r.Context(), reader: r.Body, user: limit.in, global: s.inbound}, r.ContentLength+1))
	if err != nil || int64(len(body)) != r.ContentLength {
		s.reject(w, 400, "incomplete_chunk")
		return
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != hash {
		s.reject(w, 422, "checksum_mismatch")
		return
	}
	f, err := os.OpenFile(s.uploadPath(u.ID), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		s.reject(w, 500, "storage_error")
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.Size() < u.Offset {
		s.reject(w, 500, "corrupt_upload")
		return
	}
	if err = f.Truncate(u.Offset); err == nil {
		_, err = f.WriteAt(body, u.Offset)
	}
	if err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = syncDirectory(filepath.Dir(f.Name()))
	}
	if err == nil {
		err = s.store.CheckpointFile(r.Context(), u.ID, u.Offset, u.Offset+int64(len(body)))
	}
	if err != nil {
		// A cancelled SQL call can have committed its checkpoint. Keep the bytes;
		// the next request/startup truncates only beyond the recorded offset.
		s.reject(w, 500, "storage_error")
		return
	}
	s.received.Add(uint64(len(body)))
	u.Offset += int64(len(body))
	jsonResponse(w, u)
}

func (s *Server) complete(w http.ResponseWriter, r *http.Request, u persist.FileUpload) {
	result, err := s.enqueueVerification(r.Context(), u.ID)
	if errors.Is(err, errVerificationBusy) {
		s.reject(w, 429, "verification_busy")
		return
	}
	if err != nil {
		s.reject(w, 409, "incomplete_upload")
		return
	}
	if result.State == "verifying" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
	}
	jsonResponse(w, result)
}
func verifyBlob(ctx context.Context, path string, size int64, hash string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	if stat.Size() != size {
		return errors.New("blob size mismatch")
	}
	h := sha256.New()
	if _, err = io.Copy(h, &contextReader{ctx, f}); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != hash {
		return errors.New("blob checksum mismatch")
	}
	return nil
}

func (s *Server) attachment(ctx context.Context, a voice.Session, id string) (persist.FileAttachment, error) {
	n, e := strconv.ParseInt(id, 10, 64)
	if e != nil || n <= 0 {
		return persist.FileAttachment{}, errors.New("invalid ID")
	}
	att, e := s.store.FileAttachment(ctx, n)
	if e != nil {
		return att, e
	}
	if att.MessageID == 0 && att.UserID != a.UserID {
		return att, errors.New("unattached")
	}
	target := domain.ChatTarget{Kind: att.Kind, ID: att.TargetID}
	if att.Kind == domain.ChatDirect {
		if a.UserID != att.UserID && a.UserID != att.TargetID {
			return att, errors.New("unavailable")
		}
		if a.UserID == att.TargetID {
			target.ID = att.UserID
		}
	}
	return att, s.check(ctx, a, target)
}
func (s *Server) download(w http.ResponseWriter, r *http.Request, a persist.FileAttachment, u *userLimit) {
	s.files.RLock()
	f, err := os.Open(s.blobPath(a.SHA256))
	s.files.RUnlock()
	if err != nil {
		s.reject(w, 500, "missing_blob")
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.Size() != a.Size {
		s.reject(w, 500, "corrupt_blob")
		return
	}
	if strings.Contains(r.Header.Get("Range"), ",") {
		s.reject(w, 416, "multiple_ranges_unsupported")
		return
	}
	w.Header().Set("ETag", "\""+a.SHA256+"\"")
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": a.Name}))
	writer := &pacedWriter{ResponseWriter: w, ctx: r.Context(), user: u.out, global: s.outbound, sent: &s.sent}
	http.ServeContent(writer, r, a.Name, time.Time{}, f)
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(b)
}

type pacedReader struct {
	ctx          context.Context
	reader       io.Reader
	user, global *rate.Limiter
}

func (r *pacedReader) Read(b []byte) (int, error) {
	if len(b) > 32<<10 {
		b = b[:32<<10]
	}
	if err := r.user.WaitN(r.ctx, len(b)); err != nil {
		return 0, err
	}
	if err := r.global.WaitN(r.ctx, len(b)); err != nil {
		return 0, err
	}
	return r.reader.Read(b)
}

type pacedWriter struct {
	http.ResponseWriter
	ctx          context.Context
	user, global *rate.Limiter
	sent         *atomic.Uint64
}

func (w *pacedWriter) Write(b []byte) (int, error) {
	total := 0
	for len(b) > 0 {
		n := min(len(b), 32<<10)
		if err := w.user.WaitN(w.ctx, n); err != nil {
			return total, err
		}
		if err := w.global.WaitN(w.ctx, n); err != nil {
			return total, err
		}
		written, err := w.ResponseWriter.Write(b[:n])
		total += written
		w.sent.Add(uint64(written))
		if err != nil {
			return total, err
		}
		if written != n {
			return total, io.ErrShortWrite
		}
		b = b[n:]
	}
	return total, nil
}

func (s *Server) recover(ctx context.Context) error {
	list, err := s.store.FileUploads(ctx)
	if err != nil {
		return err
	}
	for _, u := range list {
		if len(u.ID) != 32 || !validHex(u.ID) || !validHash(u.SHA256) {
			return errors.New("invalid persisted file identity")
		}
		if u.State != "uploading" {
			continue
		}
		path := s.uploadPath(u.ID)
		if _, e := os.Stat(path); errors.Is(e, os.ErrNotExist) && u.Offset == u.Size {
			if e := verifyBlob(ctx, s.blobPath(u.SHA256), u.Size, u.SHA256); e == nil {
				if _, e = s.store.CompleteFile(ctx, u.ID); e != nil {
					return e
				}
				continue
			}
		}
		f, e := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
		if e != nil {
			return e
		}
		stat, e := f.Stat()
		if e == nil && stat.Size() < u.Offset {
			e = s.store.CancelFile(ctx, u.ID)
		} else if e == nil {
			e = f.Truncate(u.Offset)
		}
		_ = f.Close()
		if e != nil {
			return e
		}
	}
	return nil
}
func (s *Server) Run(ctx context.Context) {
	var workers sync.WaitGroup
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); s.verificationWorker(ctx) }()
	}
	defer workers.Wait()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	nextCleanup := time.Now().Add(time.Minute)
	lastCleanup := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ids, err := s.store.PendingFileVerifications(ctx)
			if err != nil {
				log.Printf("files pending verification: %v", err)
			} else {
				for _, id := range ids {
					if _, err = s.enqueueVerification(ctx, id); errors.Is(err, errVerificationBusy) {
						break
					}
				}
			}
			now := time.Now()
			if now.Before(nextCleanup) {
				continue
			}
			// A waiting writer blocks new readers, so one slow chunk would stall
			// every transfer. Retry each tick until the readers are gone, and
			// wait for them only when cleanup has been deferred too long.
			if now.Sub(lastCleanup) >= cleanupMaxDefer {
				s.files.Lock()
			} else if !s.files.TryLock() {
				continue
			}
			err = s.cleanupLocked(ctx)
			s.files.Unlock()
			lastCleanup = time.Now()
			nextCleanup = lastCleanup.Add(time.Minute)
			if err != nil {
				log.Printf("files cleanup failed: %v", err)
			}
		}
	}
}

// cleanupMaxDefer bounds how long active transfers can postpone cleanup.
const cleanupMaxDefer = 15 * time.Minute

func (s *Server) Cleanup(ctx context.Context) error {
	s.files.Lock()
	defer s.files.Unlock()
	return s.cleanupLocked(ctx)
}

// cleanupLocked requires s.files held for writing.
func (s *Server) cleanupLocked(ctx context.Context) error {
	if err := s.store.ExpireFiles(ctx, s.settings().UploadTTL, s.settings().Retention); err != nil {
		return err
	}
	keep, temps, err := s.store.FileCleanupRefs(ctx)
	if err != nil {
		return err
	}
	for dir, refs := range map[string]map[string]bool{"blobs": keep, "tmp": temps} {
		entries, e := os.ReadDir(filepath.Join(s.settings().Directory, dir))
		if e != nil {
			return e
		}
		for _, entry := range entries {
			if !refs[entry.Name()] && !entry.IsDir() {
				if e = os.Remove(filepath.Join(s.settings().Directory, dir, entry.Name())); e != nil {
					return e
				}
			}
		}
	}
	return nil
}

// Ensure bounded preview responses cannot become HTML documents.
func (s *Server) preview(w http.ResponseWriter, r *http.Request, a persist.FileAttachment) {
	select {
	case s.previewSlot <- struct{}{}:
		defer func() { <-s.previewSlot }()
	default:
		s.reject(w, 429, "preview_busy")
		return
	}
	s.files.RLock()
	data, err := s.thumbnail(r.Context(), a)
	s.files.RUnlock()
	if err != nil {
		s.reject(w, 415, "preview_unavailable")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	_, _ = io.Copy(w, bytes.NewReader(data))
}
func (s *Server) Metrics() map[string]uint64 {
	return map[string]uint64{"requests": s.requests.Load(), "failures": s.failures.Load(), "receivedBytes": s.received.Load(), "sentBytes": s.sent.Load(), "activeTransfers": uint64(len(s.slots))}
}
