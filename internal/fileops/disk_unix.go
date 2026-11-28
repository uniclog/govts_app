//go:build !windows

package fileops

import "os"

func SyncDirectory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func PublishFile(source, destination string) error { return os.Rename(source, destination) }
