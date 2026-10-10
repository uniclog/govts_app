package media

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"uniclog.io/sonoryx/internal/voice"
)

func TestHTTPHandlerRequiresBoundSessionCredential(t *testing.T) {
	hub := voice.NewHub()
	session, err := hub.CreateSession("alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(hub)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	handler, err := NewHTTPHandler(hub, manager)
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/media/publish", strings.NewReader(`{"offer":{"type":"offer","sdp":"bad"}}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", response.Code)
	}

	credential, err := hub.MediaCredential(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "/media/publish", strings.NewReader(`{"offer":{"type":"offer","sdp":"bad"}}`))
	request.Header.Set("X-Govts-Session", strconv.FormatUint(session.ID, 10))
	request.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(credential[:]))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("authenticated invalid offer status = %d body=%q", response.Code, response.Body.String())
	}
}
