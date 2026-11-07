package wailsui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"sync"
	"time"

	"uniclog.io/sonoryx/internal/audio"
)

// Stream sound bridge: Go captures PCM (WASAPI process loopback on Windows,
// a Core Audio process tap on macOS) and the
// publishing WebView pulls it with ReadScreenAudio, turns it into a WebRTC
// track and sends it next to the screen video. One capture runs at a time.

const (
	screenAudioReadWait = 100 * time.Millisecond
	// A capture nobody reads (reloaded or crashed page) is stopped.
	screenAudioIdle = 3 * time.Second
)

type ScreenAudioAppDTO struct {
	PID   uint32 `json:"pid"`
	Name  string `json:"name"`
	Title string `json:"title"`
}

type screenAudioSession struct {
	id       string
	capture  *audio.LoopbackCapture
	mu       sync.Mutex
	lastRead time.Time
	done     chan struct{}
	once     sync.Once
}

type screenAudioState struct {
	mu      sync.Mutex
	current *screenAudioSession
}

var screenAudio screenAudioState

// ScreenAudioSupported reports whether sound can be captured per process,
// so Sonoryx's own voice chat stays out of the stream.
func (s *Service) ScreenAudioSupported() bool { return audio.LoopbackSupported() }

func (s *Service) ScreenAudioApps() ([]ScreenAudioAppDTO, error) {
	apps, err := audio.LoopbackApps()
	if err != nil {
		return nil, err
	}
	result := make([]ScreenAudioAppDTO, 0, len(apps))
	for _, app := range apps {
		result = append(result, ScreenAudioAppDTO{PID: app.ProcessID, Name: app.Name, Title: app.Title})
	}
	return result, nil
}

// StartScreenAudio captures one app's sound (pid) or, with pid 0, all sound
// except Sonoryx. It replaces a previous capture and returns its id.
func (s *Service) StartScreenAudio(pid uint32) (string, error) {
	target := audio.LoopbackTarget{ProcessID: pid}
	if pid == 0 {
		target = audio.LoopbackTarget{ProcessID: audio.CurrentProcessID(), Exclude: true}
	} else if pid == audio.CurrentProcessID() {
		return "", errors.New("cannot capture Sonoryx itself")
	}
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	screenAudio.stop("")
	capture, err := audio.StartLoopback(target)
	if err != nil {
		log.Printf("screen audio start failed: pid=%d exclude=%t error=%v", target.ProcessID, target.Exclude, err)
		return "", err
	}
	session := &screenAudioSession{id: hex.EncodeToString(raw[:]), capture: capture, lastRead: time.Now(), done: make(chan struct{})}
	screenAudio.mu.Lock()
	previous := screenAudio.current
	screenAudio.current = session
	screenAudio.mu.Unlock()
	if previous != nil {
		previous.close()
	}
	go session.watch()
	log.Printf("screen audio started: pid=%d exclude=%t", target.ProcessID, target.Exclude)
	return session.id, nil
}

// ReadScreenAudio returns up to 250 ms of 48 kHz stereo s16le PCM (base64 in
// JSON), waiting at most 100 ms; empty while the source is silent.
func (s *Service) ReadScreenAudio(id string) ([]byte, error) {
	session := screenAudio.find(id)
	if session == nil {
		return nil, errors.New("screen audio stopped")
	}
	session.mu.Lock()
	session.lastRead = time.Now()
	session.mu.Unlock()
	pcm, err := session.capture.Read(context.Background(), screenAudioReadWait)
	if err != nil {
		screenAudio.stop(id)
		return nil, err
	}
	return pcm, nil
}

func (s *Service) StopScreenAudio(id string) { screenAudio.stop(id) }

func (st *screenAudioState) find(id string) *screenAudioSession {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.current != nil && st.current.id == id {
		return st.current
	}
	return nil
}

// stop ends the capture with this id, or any capture for "".
func (st *screenAudioState) stop(id string) {
	st.mu.Lock()
	session := st.current
	if session == nil || (id != "" && session.id != id) {
		st.mu.Unlock()
		return
	}
	st.current = nil
	st.mu.Unlock()
	session.close()
}

func (session *screenAudioSession) close() {
	session.once.Do(func() {
		close(session.done)
		session.capture.Close()
		log.Printf("screen audio stopped: dropped_bytes=%d", session.capture.Dropped())
	})
}

func (session *screenAudioSession) watch() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-session.done:
			return
		case <-ticker.C:
			session.mu.Lock()
			idle := time.Since(session.lastRead) > screenAudioIdle
			session.mu.Unlock()
			if idle {
				log.Printf("screen audio idle; stopping")
				screenAudio.stop(session.id)
				return
			}
		}
	}
}
