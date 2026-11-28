package server

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"uniclog.io/sonoryx/apps/server/internal/persist"
)

func pendingActivation(t *testing.T) (string, string, <-chan error) {
	t.Helper()
	dir := t.TempDir()
	store, err := persist.Open(filepath.Join(dir, "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	done := make(chan error, 1)
	ready, activate := filepath.Join(dir, "ready"), filepath.Join(dir, "activate")
	go func() { done <- waitDeployActivation(ctx, store, ready, activate); close(done) }()
	t.Cleanup(func() {
		cancel()
		for range done {
		}
		store.Close()
	})
	deadline := time.Now().Add(3 * time.Second)
	for {
		if data, readErr := os.ReadFile(ready); readErr == nil && strings.TrimSpace(string(data)) == strconv.Itoa(os.Getpid())+" ready" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("readiness file was not created")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatal("server did not wait for activation", err)
	default:
	}
	return ready, activate, done
}

func TestDeployWaitsForMatchingPIDBeforeActivation(t *testing.T) {
	ready, activate, done := pendingActivation(t)
	pid := strconv.Itoa(os.Getpid())
	data, err := os.ReadFile(ready)
	if err != nil || strings.TrimSpace(string(data)) != pid+" ready" {
		t.Fatal("invalid readiness", err)
	}
	if err = os.WriteFile(activate, []byte(pid), 0600); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(ready)
	if err != nil || strings.TrimSpace(string(data)) != pid+" active" {
		t.Fatal("activation not acknowledged", err)
	}
}

func TestDeployRejectsActivationForAnotherProcess(t *testing.T) {
	ready, activate, done := pendingActivation(t)
	if err := os.WriteFile(activate, []byte("0"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("activation for another PID was accepted")
	}
	data, err := os.ReadFile(ready)
	if err != nil || strings.Contains(string(data), "active") {
		t.Fatal("rejected activation opened admission", err)
	}
}
