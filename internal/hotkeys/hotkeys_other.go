//go:build !windows

package hotkeys

// Global hotkeys are implemented only on Windows; elsewhere they are ignored.
type noopBackend struct{}

func startBackend(func(action string)) (backend, error) { return noopBackend{}, nil }

func (noopBackend) apply([]Binding) map[string]error { return nil }

func (noopBackend) close() {}
