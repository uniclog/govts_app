//go:build windows

package fileops

import "golang.org/x/sys/windows"

func SyncDirectory(path string) error { return nil } // Publication uses MoveFileEx WRITE_THROUGH after File.Sync.

func PublishFile(source, destination string) error {
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
