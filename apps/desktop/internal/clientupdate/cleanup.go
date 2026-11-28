package clientupdate

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Windows may keep the old executable mapped while the swap helper exits.
// Remove only timestamped siblings belonging to this executable, after the
// frontend has started. Recovery uses its own independent backup.
func cleanupReplacedExecutables(ctx context.Context) {
	self, err := os.Executable()
	if err != nil {
		return
	}
	dir := filepath.Dir(self)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := filepath.Base(self) + ".old."
	var pending []string
	for _, entry := range entries {
		name := entry.Name()
		if !entry.Type().IsRegular() || len(name) <= len(prefix) || !strings.EqualFold(name[:len(prefix)], prefix) {
			continue
		}
		suffix := name[len(prefix):]
		stamp, err := strconv.ParseInt(suffix, 10, 64)
		if err != nil || stamp <= 0 || strconv.FormatInt(stamp, 10) != suffix {
			continue
		}
		pending = append(pending, filepath.Join(dir, name))
	}
	for attempt := 0; attempt < 12 && len(pending) > 0; attempt++ {
		remaining := pending[:0]
		for _, path := range pending {
			if ctx.Err() != nil {
				return
			}
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				remaining = append(remaining, path)
			}
		}
		pending = remaining
		if len(pending) == 0 {
			return
		}
		if attempt < 11 {
			timer := time.NewTimer(5 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
	for _, path := range pending {
		log.Printf("update cleanup: old executable remains locked or inaccessible: %s", path)
	}
}
