package fileops

import "path/filepath"

// CommitFile publishes a closed, synced temporary file on the same filesystem.
func CommitFile(source, destination string) error {
	if err := PublishFile(source, destination); err != nil {
		return err
	}
	return SyncDirectory(filepath.Dir(destination))
}
