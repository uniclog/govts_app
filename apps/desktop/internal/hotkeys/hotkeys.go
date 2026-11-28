// Package hotkeys listens for system-wide physical-key shortcuts, including
// while the window is hidden in the tray. Right Alt is reserved for AltGr.
package hotkeys

import (
	"log"
	"sync"
)

// Binding maps an action to a complete shortcut, such as Ctrl+M or F1.
type Binding struct {
	Action string
	Key    string
}

// Manager owns the shortcuts and calls the handler for each press.
// A press that arrives while the handler is still running is dropped, so a
// slow action cannot replay a burst of toggles after the keys are released.
type Manager struct {
	// opMu serializes backend operations. The listener never acquires it.
	opMu    sync.Mutex
	mu      sync.Mutex
	closed  bool
	busy    bool
	actions chan string
	done    chan struct{}
	backend backend
}

// Start begins listening. Nothing is captured until Apply. Close releases it.
func Start(handler func(action string)) (*Manager, error) {
	m := &Manager{actions: make(chan string, 1), done: make(chan struct{})}
	backend, err := startBackend(m.dispatch)
	if err != nil {
		return nil, err
	}
	m.backend = backend
	go func() {
		defer close(m.done)
		for action := range m.actions {
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						log.Printf("hotkey handler panicked: %v", recovered)
					}
				}()
				handler(action)
			}()
			m.mu.Lock()
			m.busy = false
			m.mu.Unlock()
		}
	}()
	return m, nil
}

// dispatch runs on the key listener's thread and must not block it.
func (m *Manager) dispatch(action string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.busy {
		return
	}
	m.busy = true
	m.actions <- action
}

// Apply replaces every shortcut. An empty list captures nothing, so other
// programs keep the keys. The result lists bindings that could not be armed.
func (m *Manager) Apply(bindings []Binding) map[string]error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return nil
	}
	return m.backend.apply(bindings)
}

func (m *Manager) Close() {
	m.opMu.Lock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		m.opMu.Unlock()
		<-m.done
		return
	}
	m.closed = true
	m.mu.Unlock()
	m.backend.close()
	m.mu.Lock()
	close(m.actions)
	m.mu.Unlock()
	m.opMu.Unlock()
	<-m.done
}

type backend interface {
	apply(bindings []Binding) map[string]error
	// close returns after the last dispatch call.
	close()
}
