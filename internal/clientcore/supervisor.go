package clientcore

import (
	"context"
	"errors"
	"io"
	"log"
	"sync"
)

type loopSupervisor struct {
	ctx    context.Context
	cancel context.CancelFunc

	wg       sync.WaitGroup
	done     chan struct{}
	doneOnce sync.Once

	mu     sync.Mutex
	errors []error

	stopContextWakeup func() bool
}

func newLoopSupervisor(parent context.Context) *loopSupervisor {
	ctx, cancel := context.WithCancel(parent)
	supervisor := &loopSupervisor{
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	supervisor.stopContextWakeup = context.AfterFunc(ctx, supervisor.signalDone)
	return supervisor
}

func (s *loopSupervisor) Context() context.Context {
	return s.ctx
}

func (s *loopSupervisor) Cancel() {
	s.cancel()
}

func (s *loopSupervisor) GoNamed(name string, loop func(context.Context) error) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		err := unexpectedLoopError(loop(s.ctx))
		if err != nil || s.ctx.Err() == nil {
			log.Printf("session loop ended: operation=%s error=%v", name, err)
		}
		if err != nil {
			s.mu.Lock()
			s.errors = append(s.errors, err)
			s.mu.Unlock()
		}

		s.signalDone()
		s.cancel()
	}()
}

func (s *loopSupervisor) Wait(shutdown func() error) error {
	<-s.done
	return s.finish(shutdown)
}

func (s *loopSupervisor) Shutdown(shutdown func() error) error {
	s.cancel()
	s.signalDone()
	return s.finish(shutdown)
}

func (s *loopSupervisor) finish(shutdown func() error) error {
	s.cancel()
	shutdownErr := shutdown()
	s.wg.Wait()
	s.stopContextWakeup()

	s.mu.Lock()
	loopErrors := append([]error(nil), s.errors...)
	s.mu.Unlock()

	return errors.Join(errors.Join(loopErrors...), shutdownErr)
}

func (s *loopSupervisor) signalDone() {
	s.doneOnce.Do(func() {
		close(s.done)
	})
}

func unexpectedLoopError(err error) error {
	if err == nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrClosedPipe) {
		return nil
	}
	return err
}
