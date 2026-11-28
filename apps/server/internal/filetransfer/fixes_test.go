package filetransfer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"uniclog.io/sonoryx/apps/server/internal/persist"
	"uniclog.io/sonoryx/apps/server/internal/voice"
)

type checkedBodyReader struct {
	io.Reader
	check func()
}

func (r checkedBodyReader) Read(p []byte) (int, error) {
	r.check()
	return r.Reader.Read(p)
}

func TestAdminReadsBodyOutsidePolicyGate(t *testing.T) {
	f := fileFixture(t)
	if err := f.store.SetOwner(context.Background(), f.users[0].ID); err != nil {
		t.Fatal(err)
	}
	reads := 0
	body := checkedBodyReader{Reader: strings.NewReader(`{"action":"rights","id":"` + fmt.Sprint(f.users[1].ID) + `","upload":false,"download":false}`), check: func() {
		reads++
		if !f.s.policyGate.TryLock() {
			t.Error("network body read holds the shared voice policy gate")
			return
		}
		f.s.policyGate.Unlock()
	}}
	r := httptest.NewRequest("POST", "/files/v1/admin", body)
	r.Header.Set("X-Govts-Session", "1")
	r.Header.Set("Authorization", "Bearer "+f.tokens[0])
	w := httptest.NewRecorder()
	f.s.ServeHTTP(w, r)
	expectStatus(t, w, 204)
	if reads == 0 {
		t.Fatal("request body was not read")
	}
}

func TestFileRightsHTTPEnforcesAccountHierarchy(t *testing.T) {
	f := fileFixture(t)
	ctx := context.Background()
	if err := f.store.SetOwner(ctx, f.users[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SetPermission(ctx, f.users[1].ID, persist.PermissionAdmin, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.SetPermission(ctx, f.users[2].ID, persist.PermissionAdmin, true); err != nil {
		t.Fatal(err)
	}
	set := func(target int, status int) {
		t.Helper()
		body := []byte(fmt.Sprintf(`{"action":"rights","id":"%d","upload":false,"download":false}`, f.users[target].ID))
		expectStatus(t, f.call("POST", "/files/v1/admin", 1, body, nil), status)
	}
	for _, target := range []int{0, 1, 2} {
		set(target, 403)
		up, down, err := f.store.FileRights(ctx, f.users[target].ID)
		if err != nil || !up || !down {
			t.Fatal("protected rights changed", target, err)
		}
	}
	if _, err := f.store.SetPermission(ctx, f.users[2].ID, persist.PermissionAdmin, false); err != nil {
		t.Fatal(err)
	}
	if err := f.store.SetJoinLevel(ctx, f.users[2].ID, 10); err != nil {
		t.Fatal(err)
	}
	set(2, 403)
	if err := f.store.SetJoinLevel(ctx, f.users[2].ID, 0); err != nil {
		t.Fatal(err)
	}
	set(2, 204)
	up, down, err := f.store.FileRights(ctx, f.users[2].ID)
	if err != nil || up || down {
		t.Fatal("allowed rights not saved", err)
	}
}

func TestConcurrentFilePoliciesRejectStaleRevision(t *testing.T) {
	f := fileFixture(t)
	ctx := context.Background()
	if err := f.store.SetOwner(ctx, f.users[0].ID); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	codes := make(chan int, 2)
	var workers sync.WaitGroup
	for _, parallel := range []int{12, 16} {
		config := f.s.settings()
		config.MaxTransfers = parallel
		workers.Add(1)
		go func() {
			defer workers.Done()
			body, _ := json.Marshal(map[string]any{"action": "policy", "config": config})
			<-start
			codes <- f.call("POST", "/files/v1/admin", 0, body, nil).Code
		}()
	}
	close(start)
	workers.Wait()
	close(codes)
	accepted, conflicts := 0, 0
	for code := range codes {
		switch code {
		case 204:
			accepted++
		case 409:
			conflicts++
		default:
			t.Fatalf("policy HTTP %d", code)
		}
	}
	if accepted != 1 || conflicts != 1 {
		t.Fatal("stale policy was accepted", accepted, conflicts)
	}
	stored, err := f.store.FilePolicy(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var config Config
	if err = json.Unmarshal([]byte(stored), &config); err != nil {
		t.Fatal(err)
	}
	if config.Revision != 1 || config != f.s.settings() {
		t.Fatal("runtime policy differs from database")
	}
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	for _, user := range f.s.users {
		if int(user.in.Limit()) != config.UserBytesPerSecond || int(user.out.Limit()) != config.UserBytesPerSecond {
			t.Fatal("stale user limiter")
		}
	}
}

func TestCompleteIsDurableAndRestartsWithoutAnotherComplete(t *testing.T) {
	f := fileFixture(t)
	f.stop()
	data := []byte("durable verification")
	upload := f.create(t, data)
	expectStatus(t, f.chunk(t, upload, 0, data), 200)
	for i := 0; i < 2; i++ {
		expectStatus(t, f.call("POST", "/files/v1/uploads/"+upload.ID+"/complete", 0, nil, nil), 202)
	}
	persisted, err := f.store.FileUpload(context.Background(), upload.ID)
	if err != nil || persisted.State != "verifying" || persisted.AttachmentID != 0 {
		t.Fatalf("verification was not persisted: %+v %v", persisted, err)
	}
	f.s, err = New(f.store, f.hub, voice.NewAdministration(f.store, f.hub), f.c)
	if err != nil {
		t.Fatal(err)
	}
	f.start(t)
	ready := f.awaitReady(t, upload)
	if err = verifyBlob(context.Background(), f.s.blobPath(ready.SHA256), ready.Size, ready.SHA256); err != nil {
		t.Fatal(err)
	}
	again := f.finish(t, upload)
	if again.AttachmentID != ready.AttachmentID {
		t.Fatal("duplicate attachment after restart")
	}
}

func TestCancellationBeforeVerificationDoesNotPublish(t *testing.T) {
	f := fileFixture(t)
	f.stop()
	data := []byte("cancel queued verification")
	upload := f.create(t, data)
	expectStatus(t, f.chunk(t, upload, 0, data), 200)
	expectStatus(t, f.call("POST", "/files/v1/uploads/"+upload.ID+"/complete", 0, nil, nil), 202)
	expectStatus(t, f.call("DELETE", "/files/v1/uploads/"+upload.ID, 0, nil, nil), 204)
	expectStatus(t, f.call("POST", "/files/v1/uploads/"+upload.ID+"/complete", 0, nil, nil), 409)
	persisted, err := f.store.FileUpload(context.Background(), upload.ID)
	if err != nil || persisted.State != "cancelled" || persisted.AttachmentID != 0 {
		t.Fatalf("cancelled verification published: %+v %v", persisted, err)
	}
}

func TestRemovedArchiveEndpointsAreUnavailableToOwner(t *testing.T) {
	f := fileFixture(t)
	if err := f.store.SetOwner(context.Background(), f.users[0].ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/files/v1/admin/backup", "/files/v1/admin/backups", "/files/v1/admin/restore"} {
		expectStatus(t, f.call("GET", path, 0, nil, nil), 404)
		expectStatus(t, f.call("POST", path, 0, []byte(`{}`), nil), 404)
	}
}
