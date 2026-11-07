//go:build !windows && !darwin

package hotkeys

// Global hotkeys are implemented on Windows and macOS; elsewhere they are ignored.
type noopBackend struct{}

func startBackend(func(action string)) (backend, error) { return noopBackend{}, nil }

func (noopBackend) apply([]Binding) map[string]error { return nil }

func (noopBackend) close() {}
