package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"uniclog.io/sonoryx/internal/atomicfile"
	"uniclog.io/sonoryx/internal/persist"
)

// No UDP authentication or console mutation runs until this handshake completes.
// Listeners have already bound and the schema/DB are ready before the ready file appears.
func waitDeployActivation(ctx context.Context, store *persist.Store, ready, activate string) error {
	if ready == "" && activate == "" {
		return store.Ready(ctx)
	}
	if ready == "" || activate == "" || filepath.Clean(ready) == filepath.Clean(activate) {
		return errors.New("both distinct deploy handshake files are required")
	}
	if err := store.Ready(ctx); err != nil {
		return err
	}
	pid := strconv.Itoa(os.Getpid())
	f, err := os.OpenFile(ready, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(pid + " ready\n")
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	deadline, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		data, err := os.ReadFile(activate)
		if err == nil {
			if strings.TrimSpace(string(data)) != pid {
				return errors.New("deploy activation PID mismatch")
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			out, err := os.CreateTemp(filepath.Dir(ready), ".active-*")
			if err != nil {
				return err
			}
			defer os.Remove(out.Name())
			_, err = fmt.Fprintln(out, pid, "active")
			if err == nil {
				err = out.Sync()
			}
			ce := out.Close()
			if err == nil {
				err = ce
			}
			if err != nil {
				return err
			}
			return atomicfile.Commit(out.Name(), ready)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("deploy activation: %w", deadline.Err())
		case <-ticker.C:
		}
	}
}
