package clientcore

import (
	"errors"

	"uniclog.io/sonoryx/internal/audio"
	voiceclient "uniclog.io/sonoryx/internal/clientcore/internal/live"
)

var errAudioBackendRequired = errors.New("no PCM audio backend configured")

// AudioBackend provides PCM streams. The voice session closes opened streams to
// unblock reads and writes before it waits for its workers.
type AudioBackend interface {
	NewSession() audio.Session
}

type captureAvailability interface {
	Available() bool
	SetAvailabilityHandler(func(bool))
}

func (a *App) AudioConnectionState() (active, connected, captureAvailable bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active && !a.closed, !a.closed && a.state.ConnectionStatus() == voiceclient.ConnectionConnected, a.state.Audio.CaptureAvailable()
}

func (a *App) SetMonitor(write func([]int16)) { a.state.Audio.SetMonitor(write) }

func (a *App) LogAudioDeviceChange() {
	a.events.append("audio", "Аудиоустройство изменено", 0)
}
