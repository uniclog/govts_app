//go:build !linux

package media

// Effective receive buffer size is observed only on Linux servers.
func socketReadBuffer(any) (int, bool) { return 0, false }
