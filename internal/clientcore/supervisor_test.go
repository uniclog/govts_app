package clientcore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestLoopSupervisorCancelsOtherLoopsAfterError(t *testing.T) {
	supervisor := newLoopSupervisor(context.Background())
	wantErr := errors.New("send failed")

	supervisor.GoNamed("test", func(context.Context) error {
		return wantErr
	})
	supervisor.GoNamed("test", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})

	err := supervisor.Wait(func() error { return nil })
	if !errors.Is(err, wantErr) {
		t.Fatalf("Wait() error = %v, want %v", err, wantErr)
	}
}

func TestLoopSupervisorRunsShutdownBeforeWaiting(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	supervisor := newLoopSupervisor(parent)
	blocked := make(chan struct{})
	wantErr := errors.New("close device failed")

	supervisor.GoNamed("test", func(context.Context) error {
		<-blocked
		return nil
	})

	cancelParent()
	resultCh := make(chan error, 1)
	go func() {
		resultCh <- supervisor.Wait(func() error {
			close(blocked)
			return wantErr
		})
	}()

	select {
	case err := <-resultCh:
		if !errors.Is(err, wantErr) {
			t.Fatalf("Wait() error = %v, want %v", err, wantErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait() blocked before running shutdown")
	}
}

func TestLoopSupervisorShutdownStopsWithoutLoopResult(t *testing.T) {
	supervisor := newLoopSupervisor(context.Background())
	loopStopped := make(chan struct{})

	supervisor.GoNamed("test", func(ctx context.Context) error {
		<-ctx.Done()
		close(loopStopped)
		return ctx.Err()
	})

	if err := supervisor.Shutdown(func() error { return nil }); err != nil {
		t.Fatal(err)
	}

	select {
	case <-loopStopped:
	default:
		t.Fatal("Shutdown() returned before the loop stopped")
	}
}
