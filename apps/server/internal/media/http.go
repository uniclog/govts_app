package media

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"uniclog.io/sonoryx/apps/server/internal/voice"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/mediasignal"
)

const maxSignalingBody = 256 * 1024

type mediaAuthenticator interface {
	AuthenticateMedia(uint64, [32]byte) bool
}

type HTTPHandler struct {
	hub        mediaAuthenticator
	manager    *Manager
	policyGate *sync.Mutex
}

func NewHTTPHandler(hub mediaAuthenticator, manager *Manager, gates ...*sync.Mutex) (*HTTPHandler, error) {
	if hub == nil || manager == nil {
		return nil, errors.New("hub and media manager are required")
	}
	handler := &HTTPHandler{hub: hub, manager: manager}
	if len(gates) > 0 {
		handler.policyGate = gates[0]
	}
	return handler, nil
}

func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	status := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	w = status
	var sessionID uint64
	defer func() {
		log.Printf("media HTTP request: method=%s path=%q session_id=%d remote=%q status=%d duration=%s", r.Method, r.URL.Path, sessionID, r.RemoteAddr, status.status, time.Since(started))
	}()
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var ok bool
	sessionID, ok = h.authenticate(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/media/capabilities":
		var request struct{}
		if !decodeJSON(w, r, &request) {
			return
		}
		type codecInfo struct {
			MimeType    string `json:"mimeType"`
			Fmtp        string `json:"sdpFmtpLine"`
			PayloadType uint8  `json:"payloadType"`
		}
		codecs := make([]codecInfo, 0)
		for _, c := range screenCodecs() {
			codecs = append(codecs, codecInfo{c.MimeType, c.SDPFmtpLine, uint8(c.PayloadType)})
		}
		// "audio" tells clients this server forwards stream sound; older
		// servers omit it and clients then publish video only.
		audio := screenAudioCodec()
		writeJSON(w, map[string]any{"codecs": codecs, "audio": []codecInfo{{audio.MimeType, audio.SDPFmtpLine, uint8(audio.PayloadType)}}})
	case "/media/publish":
		h.publish(w, r, sessionID)
	case "/media/subscribe":
		h.subscribe(w, r, sessionID)
	case "/media/stop":
		h.stop(w, r, sessionID)
	case "/media/unsubscribe":
		h.unsubscribe(w, r, sessionID)
	default:
		http.NotFound(w, r)
	}
}

type statusWriter struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (h *HTTPHandler) publish(w http.ResponseWriter, r *http.Request, sessionID uint64) {
	var request mediasignal.PublishRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	ctx, cancel := signalingContext(r)
	defer cancel()
	result, err := h.manager.Publish(ctx, sessionID, request.Offer)
	if err != nil {
		writeMediaError(w, err)
		return
	}
	writeJSON(w, mediasignal.PublishResponse{StreamID: strconv.FormatUint(uint64(result.StreamID), 10), Answer: result.Answer})
}

func (h *HTTPHandler) subscribe(w http.ResponseWriter, r *http.Request, sessionID uint64) {
	var request mediasignal.SubscribeRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	streamID, err := parseStreamID(request.StreamID)
	if err != nil || request.SubscriberID == "" || len(request.SubscriberID) > 128 {
		http.Error(w, "invalid subscription", http.StatusBadRequest)
		return
	}
	ctx, cancel := signalingContext(r)
	defer cancel()
	result, err := h.manager.Subscribe(ctx, sessionID, streamID, request.SubscriberID, request.Offer)
	if err != nil {
		writeMediaError(w, err)
		return
	}
	writeJSON(w, mediasignal.SubscribeResponse{Answer: result.Answer})
}

func (h *HTTPHandler) stop(w http.ResponseWriter, r *http.Request, sessionID uint64) {
	var request mediasignal.StreamRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	streamID, err := parseStreamID(request.StreamID)
	if err != nil {
		http.Error(w, "invalid stream ID", http.StatusBadRequest)
		return
	}
	h.manager.StopPublisher(sessionID, streamID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *HTTPHandler) unsubscribe(w http.ResponseWriter, r *http.Request, sessionID uint64) {
	var request mediasignal.StreamRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	streamID, err := parseStreamID(request.StreamID)
	if err != nil || request.SubscriberID == "" || len(request.SubscriberID) > 128 {
		http.Error(w, "invalid subscription", http.StatusBadRequest)
		return
	}
	h.manager.Unsubscribe(sessionID, streamID, request.SubscriberID)
	w.WriteHeader(http.StatusNoContent)
}

func (h *HTTPHandler) authenticate(r *http.Request) (uint64, bool) {
	// Clients send X-Govts-Session: the header name is part of the wire format
	// and predates the Sonoryx rename.
	header := r.Header.Get("X-Govts-Session")
	if header == "" {
		header = r.Header.Get("X-Sonoryx-Session")
	}
	sessionID, err := strconv.ParseUint(header, 10, 64)
	if err != nil || sessionID == 0 {
		return 0, false
	}
	value := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return 0, false
	}
	var credential [32]byte
	copy(credential[:], decoded)
	if h.policyGate != nil {
		h.policyGate.Lock()
		defer h.policyGate.Unlock()
	}
	return sessionID, h.hub.AuthenticateMedia(sessionID, credential)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	body := http.MaxBytesReader(w, r.Body, maxSignalingBody)
	decoder := json.NewDecoder(body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		http.Error(w, "trailing JSON", http.StatusBadRequest)
		return false
	}
	return true
}

func parseStreamID(value string) (domain.StreamID, error) {
	id, err := strconv.ParseUint(value, 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("invalid stream ID")
	}
	return domain.StreamID(id), nil
}
func writeJSON(w http.ResponseWriter, value any) {
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("encode media HTTP response failed: error=%v", err)
		http.Error(w, "encode response", http.StatusInternalServerError)
	}
}
func signalingContext(r *http.Request) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), 15*time.Second)
}

// publicError carries a message that is safe to show the client. Its cause,
// which may hold library or system detail, reaches only the server log.
type publicError struct {
	message string
	cause   error
}

func (e *publicError) Error() string {
	if e.cause == nil {
		return e.message
	}
	return e.message + ": " + e.cause.Error()
}

func (e *publicError) Unwrap() error { return e.cause }

// publicMediaErrors are sentinel errors whose own text the client may see.
var publicMediaErrors = []error{ErrSubscriptionDenied, ErrMediaLimit, ErrPublisherExists, ErrStreamNotReady, voice.ErrSessionNotInChannel}

func writeMediaError(w http.ResponseWriter, err error) {
	log.Printf("media operation failed: error=%v", err)
	status := http.StatusBadRequest
	switch {
	case errors.Is(err, ErrSubscriptionDenied):
		status = http.StatusForbidden
	case errors.Is(err, ErrMediaLimit):
		status = http.StatusTooManyRequests
	}
	message := "media operation failed"
	var public *publicError
	if errors.As(err, &public) {
		message = public.message
	}
	for _, known := range publicMediaErrors {
		if errors.Is(err, known) {
			message = known.Error()
			break
		}
	}
	http.Error(w, message, status)
}
