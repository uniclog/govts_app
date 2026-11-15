//go:build !windows

package atomicfile

import "os"

// Publish atomically replaces destination with source.
func Publish(source, destination string) error { return os.Rename(source, destination) }

// SyncDir flushes directory metadata so a published name survives a crash.
func SyncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
