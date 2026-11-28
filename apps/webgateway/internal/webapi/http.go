package webapi

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"
	"golang.org/x/time/rate"
	"uniclog.io/sonoryx/apps/webgateway/internal/config"
	"uniclog.io/sonoryx/apps/webgateway/internal/session"
	"uniclog.io/sonoryx/apps/webgateway/internal/webrtcaudio"
)

const cookieName = "__Host-sonoryx"
const browserCookieName = "__Host-sonoryx-browser"

type handler struct {
	cfg        config.Config
	sessions   *session.Manager
	attempts   *rate.Limiter
	remoteMu   sync.Mutex
	remote     map[string]remoteAttempt
	nextPrune  time.Time
	fileChunks chan struct{}
}

type remoteAttempt struct {
	limiter *rate.Limiter
	last    time.Time
}

func New(cfg config.Config, sessions *session.Manager) http.Handler {
	h := &handler{cfg: cfg, sessions: sessions, attempts: rate.NewLimiter(5, 25), remote: make(map[string]remoteAttempt), fileChunks: make(chan struct{}, 8)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/config", h.publicConfig)
	mux.HandleFunc("GET /api/session", h.current)
	mux.HandleFunc("POST /api/session", h.login)
	mux.HandleFunc("DELETE /api/session", h.logout)
	mux.HandleFunc("POST /api/webrtc/offer", h.offer)
	mux.HandleFunc("GET /api/media/capabilities", h.mediaCapabilities)
	mux.HandleFunc("POST /api/media/publish", h.mediaPublish)
	mux.HandleFunc("POST /api/media/subscribe", h.mediaSubscribe)
	mux.HandleFunc("POST /api/media/stop", h.mediaStop)
	mux.HandleFunc("POST /api/media/unsubscribe", h.mediaUnsubscribe)
	mux.HandleFunc("GET /api/events", h.events)
	mux.HandleFunc("GET /api/audio", h.getAudio)
	mux.HandleFunc("POST /api/audio", h.updateAudio)
	mux.HandleFunc("POST /api/admin", h.admin)
	mux.HandleFunc("GET /api/admin/files", h.adminFiles)
	mux.HandleFunc("POST /api/admin/files", h.adminFiles)
	mux.HandleFunc("GET /api/admin/files/rights/{id}", h.fileRights)
	mux.HandleFunc("POST /api/moderation", h.moderate)
	mux.HandleFunc("GET /api/audio/participants/{participant}", h.participantAudio)
	mux.HandleFunc("POST /api/audio/participants/{participant}", h.participantAudio)
	mux.HandleFunc("GET /api/files/attachments/{id}", h.attachment)
	mux.HandleFunc("GET /api/files/attachments/{id}/info", h.attachmentInfo)
	mux.HandleFunc("GET /api/files/attachments/{id}/preview", h.attachmentPreview)
	mux.HandleFunc("GET /api/files/capabilities", h.fileCapabilities)
	mux.HandleFunc("POST /api/files/uploads", h.startUpload)
	mux.HandleFunc("GET /api/files/uploads/{upload}", h.upload)
	mux.HandleFunc("PATCH /api/files/uploads/{upload}", h.upload)
	mux.HandleFunc("DELETE /api/files/uploads/{upload}", h.upload)
	mux.HandleFunc("POST /api/files/uploads/{upload}/complete", h.upload)
	mux.HandleFunc("GET /{$}", h.static)
	mux.HandleFunc("GET /sonoryx.svg", h.static)
	mux.HandleFunc("GET /assets/", h.static)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; img-src 'self'; media-src 'self' blob:; connect-src 'self' wss://"+strings.TrimPrefix(cfg.Origin, "https://")+"; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		displayCapture := "()"
		if cfg.MediaFingerprint != "" {
			displayCapture = "(self)"
		}
		w.Header().Set("Permissions-Policy", "microphone=(self), camera=(), speaker-selection=(self), display-capture="+displayCapture)
		w.Header().Set("Cache-Control", "no-store")
		if !strings.EqualFold(r.Host, strings.TrimPrefix(cfg.Origin, "https://")) {
			fail(w, http.StatusMisdirectedRequest, "Неверный адрес веб-шлюза")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (h *handler) publicConfig(w http.ResponseWriter, _ *http.Request) {
	jsonReply(w, http.StatusOK, struct {
		LoginMode string `json:"loginMode"`
	}{LoginMode: h.cfg.LoginMode})
}

type problem struct {
	Error string `json:"error"`
}
type sessionView struct {
	ExpiresAt time.Time          `json:"expiresAt"`
	ICE       []webrtc.ICEServer `json:"iceServers"`
}

func jsonReply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	jsonReply(w, status, problem{Error: message})
}

func (h *handler) origin(w http.ResponseWriter, r *http.Request) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 1 && origins[0] == h.cfg.Origin {
		return true
	}
	fail(w, http.StatusForbidden, "Недопустимый Origin")
	return false
}

func (h *handler) lookup(r *http.Request) *session.Session {
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return nil
	}
	return h.sessions.Lookup(cookie.Value)
}
func (h *handler) require(w http.ResponseWriter, r *http.Request) *session.Session {
	s := h.lookup(r)
	if s == nil {
		fail(w, http.StatusUnauthorized, session.ErrExpired.Error())
	}
	return s
}
func (h *handler) view(s *session.Session) sessionView {
	return sessionView{ExpiresAt: s.Expires(), ICE: append([]webrtc.ICEServer{}, h.cfg.ICE...)}
}
func (h *handler) current(w http.ResponseWriter, r *http.Request) {
	if s := h.require(w, r); s != nil {
		jsonReply(w, http.StatusOK, h.view(s))
	}
}
func (h *handler) allowLogin(remote string) bool {
	if !h.attempts.Allow() {
		return false
	}
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
		host = ip.Mask(net.CIDRMask(64, 128)).String()
	}
	now := time.Now()
	h.remoteMu.Lock()
	defer h.remoteMu.Unlock()
	if now.After(h.nextPrune) {
		for key, attempt := range h.remote {
			if now.Sub(attempt.last) > 5*time.Minute {
				delete(h.remote, key)
			}
		}
		h.nextPrune = now.Add(time.Minute)
	}
	attempt, exists := h.remote[host]
	if !exists {
		if len(h.remote) >= 4096 {
			return false
		}
		attempt.limiter = rate.NewLimiter(rate.Every(5*time.Second), 8)
	}
	attempt.last = now
	h.remote[host] = attempt
	return attempt.limiter.Allow()
}

func (h *handler) login(w http.ResponseWriter, r *http.Request) {
	if !h.origin(w, r) {
		return
	}
	if !h.allowLogin(r.RemoteAddr) {
		fail(w, http.StatusTooManyRequests, session.ErrRate.Error())
		return
	}
	if h.lookup(r) != nil {
		fail(w, http.StatusConflict, session.ErrDuplicate.Error())
		return
	}
	var request struct {
		Nickname   string `json:"nickname"`
		Account    string `json:"account"`
		AccessCode string `json:"accessCode"`
	}
	if !readJSON(w, r, 1024, &request) {
		return
	}
	var s *session.Session
	var token, browserToken string
	var err error
	if h.cfg.LoginMode == "nickname" {
		if request.Account != "" || request.AccessCode != "" {
			fail(w, http.StatusBadRequest, "Укажите никнейм")
			return
		}
		var previous string
		if cookie, cookieErr := r.Cookie(browserCookieName); cookieErr == nil {
			previous = cookie.Value
		}
		s, token, browserToken, err = h.sessions.LoginNickname(request.Nickname, previous)
	} else {
		if request.Nickname != "" {
			fail(w, http.StatusBadRequest, "Укажите аккаунт и код доступа")
			return
		}
		s, token, err = h.sessions.Login(request.Account, request.AccessCode)
	}
	switch {
	case errors.Is(err, session.ErrNickname):
		fail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, session.ErrCredentials):
		fail(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, session.ErrDuplicate):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, session.ErrRate):
		fail(w, http.StatusTooManyRequests, err.Error())
	case errors.Is(err, session.ErrCapacity), errors.Is(err, session.ErrExpired):
		fail(w, http.StatusServiceUnavailable, err.Error())
	case err != nil:
		log.Print("web login failed to start Sonoryx connection")
		fail(w, http.StatusInternalServerError, "Не удалось начать подключение к Sonoryx")
	default:
		if browserToken != "" {
			setCookie(w, browserCookieName, browserToken, time.Now().AddDate(1, 0, 0), 0)
		}
		setCookie(w, cookieName, token, s.Expires(), 0)
		jsonReply(w, http.StatusCreated, h.view(s))
	}
}
func setCookie(w http.ResponseWriter, name, value string, expires time.Time, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: maxAge})
}
func (h *handler) logout(w http.ResponseWriter, r *http.Request) {
	if !h.origin(w, r) {
		return
	}
	if s := h.lookup(r); s != nil {
		s.Logout()
	}
	setCookie(w, cookieName, "", time.Unix(1, 0), -1)
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) offer(w http.ResponseWriter, r *http.Request) {
	if !h.origin(w, r) {
		return
	}
	s := h.require(w, r)
	if s == nil {
		return
	}
	var request webrtc.SessionDescription
	if !readJSON(w, r, 64<<10, &request) {
		return
	}
	answer, err := s.Offer(r.Context(), request)
	switch {
	case errors.Is(err, webrtcaudio.ErrOffer):
		fail(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, session.ErrPeerBusy), errors.Is(err, session.ErrNoSocket):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, session.ErrExpired):
		fail(w, http.StatusUnauthorized, err.Error())
	case err != nil:
		fail(w, http.StatusServiceUnavailable, "Не удалось согласовать голос. Повторите попытку")
	default:
		jsonReply(w, http.StatusOK, answer)
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, limit int64, value any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		fail(w, http.StatusUnsupportedMediaType, "Ожидается application/json")
		return false
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil || decoder.Decode(new(any)) != io.EOF {
		fail(w, http.StatusBadRequest, "Некорректное тело запроса")
		return false
	}
	return true
}

func (h *handler) static(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if path != "index.html" && path != "sonoryx.svg" && !strings.HasPrefix(path, "assets/") {
		http.NotFound(w, r)
		return
	}
	if !strings.EqualFold(filepath.ToSlash(filepath.Clean(path)), path) {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(filepath.Join(h.cfg.WebRoot, filepath.FromSlash(path)))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	if strings.HasPrefix(path, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}
