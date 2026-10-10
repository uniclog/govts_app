package clientupdate

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"
	"github.com/wailsapp/wails/v3/pkg/updater"
	"uniclog.io/sonoryx/internal/appversion"
)

type Snapshot struct {
	Current            string `json:"current"`
	Available          string `json:"available"`
	MinServer          string `json:"minServer"`
	AvailableMinServer string `json:"availableMinServer"`
	Status             string `json:"status"`
	Error              string `json:"error"`
	Written            int64  `json:"written"`
	Total              int64  `json:"total"`
	ReleaseURL         string `json:"releaseURL"`
}

type Service struct {
	mu       sync.Mutex
	view     Snapshot
	u        *updater.Updater
	profile  string
	busy     func() bool
	cancel   context.CancelFunc
	root     context.Context
	stop     context.CancelFunc
	unlisten func()
	startup  sync.Once
}

func New(profile string, busy func() bool) *Service {
	return &Service{profile: profile, busy: busy, view: Snapshot{Status: "idle"}}
}

func Initialize(s *Service, app *application.App, version, publicKey string) error {
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(publicKey))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return errors.New("неверный открытый ключ обновлений")
	}
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" || len(via) >= 10 {
			return errors.New("неверное перенаправление обновления")
		}
		return nil
	}}
	s.u = app.Updater
	s.root, s.stop = context.WithCancel(context.Background())
	s.view.Current = version
	s.view.MinServer = appversion.MinimumServerVersion
	if result, err := os.ReadFile(filepath.Join(s.profile, "update-recovery-result.txt")); err == nil {
		s.view.Error = string(result)
	}
	if err := s.u.Init(updater.Config{CurrentVersion: version, Providers: []updater.Provider{&provider{key: key, client: client, busy: s.busy}}, PublicKey: key, Window: updater.WindowNone}); err != nil {
		return err
	}
	s.unlisten = app.Event.On(updater.EventDownloadProgress, func(e *application.CustomEvent) {
		if p, ok := e.Data.(updater.Progress); ok {
			s.mu.Lock()
			s.view.Written, s.view.Total = p.Written, p.Total
			s.mu.Unlock()
		}
	})
	go s.loop()
	return nil
}

// The loop only discovers releases; downloading is always user-initiated.
// GitHub allows 60 unauthenticated API requests per hour per IP, so the
// interval leaves room for several clients behind one NAT.
const (
	firstCheckDelay = 2 * time.Second
	checkInterval   = 5 * time.Minute
)

func (s *Service) loop() {
	timer := time.NewTimer(firstCheckDelay)
	defer timer.Stop()
	for {
		select {
		case <-s.root.Done():
			return
		case <-timer.C:
		}
		switch s.Snapshot().Status {
		case "checking", "ready", "downloading", "restarting":
		default:
			if err := s.Check(); err != nil {
				log.Printf("update check: %v", err)
			}
		}
		timer.Reset(checkInterval)
	}
}

func (s *Service) Snapshot() Snapshot { s.mu.Lock(); defer s.mu.Unlock(); return s.view }

func (s *Service) ConfirmStartup() {
	s.startup.Do(func() {
		ConfirmRecoveryStartup(s.Snapshot().Current)
		if s.root != nil {
			go cleanupReplacedExecutables(s.root)
		}
	})
}

func (s *Service) begin(status string, timeout time.Duration) (context.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.u == nil {
		return nil, errors.New("обновления недоступны")
	}
	if s.cancel != nil || s.view.Status == "restarting" {
		return nil, errors.New("операция обновления уже выполняется")
	}
	if status == "checking" && s.view.Status == "ready" {
		return nil, errors.New("обновление уже готово к установке")
	}
	ctx, cancel := context.WithTimeout(s.root, timeout)
	s.cancel = cancel
	s.view.Status, s.view.Error = status, ""
	return ctx, nil
}

func (s *Service) finish(status string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.view.Status = status
	if err != nil {
		if errors.Is(err, context.Canceled) {
			s.view.Status = "available"
			if s.view.Available == "" {
				s.view.Status = "idle"
			}
			s.view.Error = ""
		} else {
			s.view.Status = "error"
			s.view.Error = err.Error()
		}
	}
}

func (s *Service) Check() error {
	ctx, err := s.begin("checking", 30*time.Second)
	if err != nil {
		return err
	}
	release, err := s.u.Check(ctx)
	status := "up-to-date"
	s.mu.Lock()
	if err == nil {
		s.view.Available, s.view.AvailableMinServer, s.view.ReleaseURL = "", "", ""
		if release != nil {
			s.view.Available = release.Version
			if minServer, _ := release.Metadata["minServerVersion"].(string); minServer != "" {
				s.view.AvailableMinServer = minServer
			}
			s.view.ReleaseURL = "https://github.com/" + repository + "/releases/tag/v" + release.Version
			status = "available"
		}
	}
	s.mu.Unlock()
	s.finish(status, err)
	return err
}

func (s *Service) Download() error {
	s.mu.Lock()
	allowed := s.view.Available != "" && (s.view.Status == "available" || s.view.Status == "error")
	s.mu.Unlock()
	if !allowed {
		return errors.New("сначала проверьте обновления")
	}
	ctx, err := s.begin("downloading", 45*time.Minute)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.view.Written = 0
	s.mu.Unlock()
	err = s.u.DownloadAndInstall(ctx)
	s.finish("ready", err)
	return err
}

func (s *Service) Cancel() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cancel != nil {
		s.cancel()
	}
}

func (s *Service) Restart() error {
	s.mu.Lock()
	if s.view.Status != "ready" || s.cancel != nil {
		s.mu.Unlock()
		return errors.New("обновление ещё не готово")
	}
	s.view.Status = "restarting"
	s.mu.Unlock()
	self, err := os.Executable()
	if err == nil {
		var f *os.File
		f, err = os.CreateTemp(filepath.Dir(self), ".govts-update-permission-*")
		if err == nil {
			path := f.Name()
			err = f.Close()
			_ = os.Remove(path)
		}
	}
	if err == nil {
		var cancelRecovery func()
		cancelRecovery, err = PrepareRecovery(s.Snapshot().Available)
		if err == nil {
			err = s.u.Restart(context.Background())
			if err != nil {
				cancelRecovery()
			}
		}
	}
	if err != nil {
		s.mu.Lock()
		s.view.Status = "ready"
		s.view.Error = fmt.Sprintf("Не удалось установить обновление: %v. Можно скачать релиз вручную.", err)
		s.mu.Unlock()
	}
	return err
}

func Stop(s *Service) {
	if s.stop != nil {
		s.stop()
	}
	s.Cancel()
	if s.unlisten != nil {
		s.unlisten()
	}
	// A staged download belongs to the swap helper only during Restart.
	// Otherwise closing the client must not leave another EXE in Temp.
	if s.u != nil && s.Snapshot().Status != "restarting" {
		path := s.u.DownloadedPath()
		if path == "" {
			return
		}
		dir, err := filepath.Abs(filepath.Dir(path))
		root, rootErr := filepath.Abs(os.TempDir())
		rel, relErr := filepath.Rel(root, dir)
		if err == nil && rootErr == nil && relErr == nil && rel == filepath.Base(dir) && strings.HasPrefix(rel, "wails-update-") {
			_ = os.RemoveAll(dir)
		}
	}
}
