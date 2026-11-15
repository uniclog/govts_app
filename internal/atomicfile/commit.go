// Package atomicfile publishes fully written temporary files durably.
package atomicfile

import "path/filepath"

// Commit publishes a closed, synced temporary file on the same filesystem.
func Commit(source, destination string) error {
	if err := Publish(source, destination); err != nil {
		return err
	}
	return SyncDir(filepath.Dir(destination))
}
