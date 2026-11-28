package media

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"uniclog.io/sonoryx/apps/server/internal/voice"
)

func TestHTTPHandlerRequiresBoundSessionCredential(t *testing.T) {
	hub := voice.NewHub()
	session, _, err := hub.CreateAuthenticatedSession("alice", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9000}, 1, 0, 0, false)
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
	request.Header.Set("X-Sonoryx-Session", strconv.FormatUint(session.ID, 10))
	request.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(credential[:]))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("authenticated invalid offer status = %d body=%q", response.Code, response.Body.String())
	}
}

func TestCapabilitiesAreAuthenticatedAndStrictJSON(t *testing.T) {
	hub := voice.NewHub()
	session, _, err := hub.CreateAuthenticatedSession("alice", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9000}, 1, 0, 0, false)
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
	credential, err := hub.MediaCredential(session.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		body       string
		authorized bool
		want       int
	}{
		{"{}", false, http.StatusUnauthorized},
		{"{}", true, http.StatusOK},
		{`{"unexpected":true}`, true, http.StatusBadRequest},
		{"{} {}", true, http.StatusBadRequest},
		{"{", true, http.StatusBadRequest},
	} {
		request := httptest.NewRequest(http.MethodPost, "/media/capabilities", strings.NewReader(test.body))
		if test.authorized {
			request.Header.Set("X-Govts-Session", strconv.FormatUint(session.ID, 10))
			request.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(credential[:]))
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != test.want {
			t.Fatalf("body=%q authorized=%v: status=%d want=%d", test.body, test.authorized, response.Code, test.want)
		}
		if response.Code == http.StatusOK {
			var capabilities struct {
				Codecs []struct {
					MimeType    string `json:"mimeType"`
					Fmtp        string `json:"sdpFmtpLine"`
					PayloadType uint8  `json:"payloadType"`
				} `json:"codecs"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &capabilities); err != nil {
				t.Fatal(err)
			}
			if len(capabilities.Codecs) != 8 {
				t.Fatalf("codec count=%d", len(capabilities.Codecs))
			}
			for i, c := range capabilities.Codecs {
				registered := screenCodecs()[i]
				if c.MimeType != registered.MimeType || c.Fmtp != registered.SDPFmtpLine || c.PayloadType != uint8(registered.PayloadType) {
					t.Fatal("advertised codec differs from registration")
				}
			}
		}
	}
}
