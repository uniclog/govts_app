package fileclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"uniclog.io/sonoryx/internal/identity"
)

func TestFilePinCheckedBeforeSendingCredentialOrBody(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing credential")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != "file bytes" {
			t.Error("unexpected body")
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	wrong := pinnedFileTransport(strings.Repeat("0", 64))
	defer wrong.CloseIdleConnections()
	client := &http.Client{Transport: wrong}
	request, _ := http.NewRequest("POST", server.URL, strings.NewReader("file bytes"))
	request.Header.Set("Authorization", "Bearer secret")
	if response, err := client.Do(request); err == nil {
		response.Body.Close()
		t.Fatal("accepted wrong pin")
	}
	if calls.Load() != 0 {
		t.Fatal("sent HTTP credentials before pin validation")
	}
	correct := pinnedFileTransport(identity.TLSFingerprint(server.Certificate()))
	defer correct.CloseIdleConnections()
	client.Transport = correct
	request, _ = http.NewRequest("POST", server.URL, strings.NewReader("file bytes"))
	request.Header.Set("Authorization", "Bearer secret")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if calls.Load() != 1 {
		t.Fatal("correct pin request missing")
	}
}
