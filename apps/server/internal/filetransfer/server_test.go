package filetransfer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"

	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"uniclog.io/sonoryx/apps/server/internal/persist"
	"uniclog.io/sonoryx/apps/server/internal/voice"
	"uniclog.io/sonoryx/internal/domain"
)

func TestMain(m *testing.M) {
	if ThumbnailMode() {
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type fixture struct {
	s      *Server
	store  *persist.Store
	hub    *voice.Hub
	users  []persist.Account
	tokens []string
	c      Config
	stop   func()
}

func fileFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	store, err := persist.Open(filepath.Join(dir, "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	if err = store.SaveInitialServer(ctx, persist.ServerState{Name: "files", DefaultChannelID: 1, Channels: []domain.Channel{{ID: 1, Name: "default", Type: domain.ChannelTypePermanent, Audio: domain.DefaultAudioProfile()}}}); err != nil {
		t.Fatal(err)
	}
	f := &fixture{store: store, hub: voice.NewHub(), c: DefaultConfig(filepath.Join(dir, "files"))}
	f.c.MinFreeBytes = 0
	f.c.UserBytesPerSecond = 1 << 30
	f.c.ServerBytesPerSecond = 1 << 30
	f.hub.SetFileEndpoint(9999, "pin")
	for i, name := range []string{"alice", "bob", "eve"} {
		user, e := store.FindOrCreateAccount(ctx, [32]byte{byte(i + 1)}, name)
		if e != nil {
			t.Fatal(e)
		}
		f.users = append(f.users, user)
		f.hub.Add(&voice.Session{ID: uint64(i + 1), UserID: user.ID, Name: name, ChannelID: 1})
		access, e := f.hub.FileAccess(uint64(i + 1))
		if e != nil {
			t.Fatal(e)
		}
		f.tokens = append(f.tokens, access.Credential)
	}
	f.s, err = New(store, f.hub, voice.NewAdministration(store, f.hub), f.c)
	if err != nil {
		t.Fatal(err)
	}
	f.start(t)
	return f
}

func (f *fixture) start(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	srv := f.s
	go func() { defer close(done); srv.Run(ctx) }()
	stop := func() { cancel(); <-done }
	f.stop = stop
	t.Cleanup(stop)
}
func (f *fixture) call(method, path string, user int, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.Header.Set("X-Govts-Session", fmt.Sprint(user+1))
	r.Header.Set("Authorization", "Bearer "+f.tokens[user])
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	f.s.ServeHTTP(w, r)
	return w
}
func expectStatus(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	if w.Code != status {
		t.Fatalf("HTTP %d, want %d: %s", w.Code, status, w.Body.String())
	}
}
func (f *fixture) create(t *testing.T, data []byte) persist.FileUpload {
	t.Helper()
	sum := sha256.Sum256(data)
	body, _ := json.Marshal(persist.FileUpload{RequestID: "request-id-000001", Kind: domain.ChatDirect, TargetID: f.users[1].ID, Name: "../payload.txt", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
	w := f.call("POST", "/files/v1/uploads", 0, body, nil)
	expectStatus(t, w, 200)
	var u persist.FileUpload
	if err := json.Unmarshal(w.Body.Bytes(), &u); err != nil {
		t.Fatal(err)
	}
	return u
}
func (f *fixture) chunk(t *testing.T, u persist.FileUpload, offset int64, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	sum := sha256.Sum256(data)
	return f.call("PATCH", "/files/v1/uploads/"+u.ID+"/content", 0, data, map[string]string{"Upload-Offset": fmt.Sprint(offset), "Upload-Checksum": hex.EncodeToString(sum[:])})
}
func (f *fixture) ready(t *testing.T, data []byte) persist.FileUpload {
	t.Helper()
	u := f.create(t, data)
	if len(data) > 0 {
		expectStatus(t, f.chunk(t, u, 0, data), 200)
	}
	return f.finish(t, u)
}

func (f *fixture) finish(t *testing.T, u persist.FileUpload) persist.FileUpload {
	t.Helper()
	w := f.call("POST", "/files/v1/uploads/"+u.ID+"/complete", 0, nil, nil)
	if w.Code != 202 && w.Code != 200 {
		t.Fatalf("complete HTTP %d: %s", w.Code, w.Body.String())
	}
	return f.awaitReady(t, u)
}

func (f *fixture) awaitReady(t *testing.T, u persist.FileUpload) persist.FileUpload {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		w := f.call("GET", "/files/v1/uploads/"+u.ID, 0, nil, nil)
		expectStatus(t, w, 200)
		if err := json.Unmarshal(w.Body.Bytes(), &u); err != nil {
			t.Fatal(err)
		}
		if u.State == "ready" || u.State == "attached" {
			if u.AttachmentID <= 0 {
				t.Fatal("missing attachment")
			}
			return u
		}
		if u.State != "verifying" || time.Now().After(deadline) {
			t.Fatalf("verification did not complete: %+v", u)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func TestUploadResumeAttachRangeAndIsolation(t *testing.T) {
	f := fileFixture(t)
	data := []byte("abcdef0123456789")
	u := f.create(t, data)
	expectStatus(t, f.chunk(t, u, 0, data[:6]), 200)
	expectStatus(t, f.chunk(t, u, 0, data[:6]), 409)
	// Simulate a partial, unacknowledged write before server restart.
	file, err := os.OpenFile(f.s.uploadPath(u.ID), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("tail"))
	file.Close()
	f.stop()
	f.s, err = New(f.store, f.hub, voice.NewAdministration(f.store, f.hub), f.c)
	if err != nil {
		t.Fatal(err)
	}
	f.start(t)
	stat, err := os.Stat(f.s.uploadPath(u.ID))
	if err != nil || stat.Size() != 6 {
		t.Fatalf("recovery size: %v %v", stat, err)
	}
	expectStatus(t, f.chunk(t, u, 6, data[6:]), 200)
	u = f.finish(t, u)
	again := f.call("POST", "/files/v1/uploads/"+u.ID+"/complete", 0, nil, nil)
	expectStatus(t, again, 200)
	var retry persist.FileUpload
	_ = json.Unmarshal(again.Body.Bytes(), &retry)
	if retry.AttachmentID != u.AttachmentID {
		t.Fatal("complete created duplicate")
	}
	meta := []byte(fmt.Sprintf(`["%d"]`, u.AttachmentID))
	expectStatus(t, f.call("POST", "/files/v1/metadata", 1, meta, nil), 404)
	_, err = f.store.SaveChatFiles(context.Background(), f.users[0].ID, "alice", domain.ChatTarget{Kind: domain.ChatDirect, ID: f.users[1].ID}, [16]byte{1}, "", []int64{u.AttachmentID})
	if err != nil {
		t.Fatal(err)
	}
	expectStatus(t, f.call("POST", "/files/v1/metadata", 1, meta, nil), 200)
	expectStatus(t, f.call("POST", "/files/v1/metadata", 2, meta, nil), 404)
	download := f.call("GET", fmt.Sprintf("/files/v1/attachments/%d/content", u.AttachmentID), 1, nil, map[string]string{"Range": "bytes=3-8", "If-Range": "\"" + u.SHA256 + "\""})
	expectStatus(t, download, 206)
	if !bytes.Equal(download.Body.Bytes(), data[3:9]) {
		t.Fatal("range differs")
	}
	expectStatus(t, f.call("DELETE", "/files/v1/uploads/"+u.ID, 0, nil, nil), 409)
	if err = f.s.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(f.s.blobPath(u.SHA256)); err != nil {
		t.Fatal("GC removed attached blob", err)
	}
}
func TestChecksumsCredentialsAndRevocation(t *testing.T) {
	f := fileFixture(t)
	u := f.create(t, []byte("abc"))
	w := f.call("PATCH", "/files/v1/uploads/"+u.ID+"/content", 0, []byte("abc"), map[string]string{"Upload-Offset": "0", "Upload-Checksum": strings.Repeat("0", 64)})
	expectStatus(t, w, 422)
	state, err := f.store.FileUpload(context.Background(), u.ID)
	if err != nil || state.Offset != 0 {
		t.Fatal("bad chunk advanced offset", err)
	}
	old := f.tokens[0]
	f.tokens[0] = strings.Repeat("A", 43)
	expectStatus(t, f.call("GET", "/files/v1/capabilities", 0, nil, nil), 401)
	f.tokens[0] = old
	f.hub.Remove(1)
	expectStatus(t, f.call("GET", "/files/v1/uploads/"+u.ID, 0, nil, nil), 401)
}
func TestCompletionRecoversAfterPublishBeforeSQLCommit(t *testing.T) {
	f := fileFixture(t)
	data := []byte("crash after rename")
	u := f.create(t, data)
	expectStatus(t, f.chunk(t, u, 0, data), 200)
	if err := publishFile(f.s.uploadPath(u.ID), f.s.blobPath(u.SHA256)); err != nil {
		t.Fatal(err)
	}
	f.finish(t, u)
}
func TestSafeFilename(t *testing.T) {
	for _, name := range []string{"../../CON.txt", "C:\\temp\\a.exe", "\u202efake.txt", strings.Repeat("я", 200), "NUL", "..."} {
		safe := SafeName(name)
		if len(safe) > 128 || safe == "" || strings.ContainsAny(safe, "/\\\u202e") {
			t.Fatalf("unsafe name %q => %q", name, safe)
		}
	}
}
func TestUploadCanBeCancelledAfterRecipientDeletion(t *testing.T) {
	f := fileFixture(t)
	u := f.create(t, []byte("pending"))
	if err := f.store.DeleteAccountBy(context.Background(), "local-console", f.users[1].ID); err != nil {
		t.Fatal(err)
	}
	expectStatus(t, f.call("GET", "/files/v1/uploads/"+u.ID, 0, nil, nil), 200)
	expectStatus(t, f.call("DELETE", "/files/v1/uploads/"+u.ID, 0, nil, nil), 204)
	expectStatus(t, f.call("DELETE", "/files/v1/uploads/"+u.ID, 0, nil, nil), 204)
}
func TestPreviewUsesChildDecoderAndKeepsOriginal(t *testing.T) {
	f := fileFixture(t)
	var data bytes.Buffer
	if err := png.Encode(&data, image.NewRGBA(image.Rect(0, 0, 12, 8))); err != nil {
		t.Fatal(err)
	}
	u := f.ready(t, data.Bytes())
	w := f.call("GET", fmt.Sprintf("/files/v1/attachments/%d/preview", u.AttachmentID), 0, nil, nil)
	expectStatus(t, w, 200)
	cfg, format, err := image.DecodeConfig(bytes.NewReader(w.Body.Bytes()))
	if err != nil || format != "jpeg" || cfg.Width != 12 || cfg.Height != 8 {
		t.Fatalf("preview %+v %s %v", cfg, format, err)
	}
	if err = verifyBlob(context.Background(), f.s.blobPath(u.SHA256), u.Size, u.SHA256); err != nil {
		t.Fatal("original modified", err)
	}
}
