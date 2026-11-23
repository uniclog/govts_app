//go:build !windows

package apppopup

type noopBackend struct{}

func newBackend(icon []byte) (backend, error) { return noopBackend{}, nil }

func (noopBackend) show(Message) error { return nil }
func (noopBackend) setLook(Look)       {}
func (noopBackend) close()             {}
