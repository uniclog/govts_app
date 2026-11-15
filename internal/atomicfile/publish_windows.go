//go:build windows

package atomicfile

import "golang.org/x/sys/windows"

// Publish atomically replaces destination with source.
func Publish(source, destination string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// SyncDir is a no-op: Publish uses MoveFileEx WRITE_THROUGH after File.Sync.
func SyncDir(path string) error { return nil }
