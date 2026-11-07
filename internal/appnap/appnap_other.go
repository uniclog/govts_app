//go:build !darwin

package appnap

// Disable is needed only on macOS, which naps background apps.
func Disable() error { return nil }
