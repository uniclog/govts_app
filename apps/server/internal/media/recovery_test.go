package media

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/rtcp"
)

func TestNACKDoesNotRequestAKeyframe(t *testing.T) {
	p, s := queuedSubscriber()
	for range 100 {
		handleFeedback(p, s, &rtcp.TransportLayerNack{})
	}
	if s.nacks.Load() != 100 || len(p.recovery) != 0 {
		t.Fatal("NACK created an additional PLI")
	}
	handleFeedback(p, s, &rtcp.PictureLossIndication{})
	handleFeedback(p, s, &rtcp.FullIntraRequest{})
	if s.plis.Load() != 1 || s.firs.Load() != 1 || len(p.recovery) != 1 || p.pliMerged.Load() != 1 {
		t.Fatal("PLI/FIR did not share the publisher recovery signal")
	}
}

func TestRecoveryCoalescesConcurrentViewersAndDefers(t *testing.T) {
	p, s := queuedSubscriber()
	const interval = 40 * time.Millisecond
	sent := make(chan time.Time, 8)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		runRecoveryLoop(p.closed, p.recovery, interval, &p.pliMerged, func() { sent <- time.Now() })
	}()
	t.Cleanup(func() { close(p.closed); <-finished })
	requestRecoveryKeyframe(p, s)
	first := awaitPLI(t, sent)
	var requests sync.WaitGroup
	for range 5 {
		requests.Go(func() {
			for range 100 {
				requestRecoveryKeyframe(p, s)
			}
		})
	}
	requests.Wait()
	second := awaitPLI(t, sent)
	if second.Sub(first) < interval {
		t.Fatalf("publisher limiter violated: %s", second.Sub(first))
	}
	if p.pliMerged.Load() == 0 {
		t.Fatal("concurrent requests were not coalesced")
	}
	select {
	case <-sent:
		t.Fatal("the single burst caused more than one deferred PLI")
	case <-time.After(2 * interval):
	}
}

func TestRecoveryCancellationStopsDeferredPLI(t *testing.T) {
	closed := make(chan struct{})
	requests := make(chan struct{}, 1)
	sent := make(chan time.Time, 8)
	done := make(chan struct{})
	var merged atomic.Uint64
	go func() {
		defer close(done)
		runRecoveryLoop(closed, requests, time.Second, &merged, func() { sent <- time.Now() })
	}()
	requests <- struct{}{}
	awaitPLI(t, sent)
	requests <- struct{}{}
	close(closed)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("closing publisher did not cancel recovery worker")
	}
	if len(sent) != 0 {
		t.Fatal("a deferred PLI was sent after closing the publisher")
	}
}

func TestQueueRecoveryIsBoundedAndClosedViewerCannotRequest(t *testing.T) {
	p, s := queuedSubscriber()
	for range 1000 {
		requestQueueRecovery(p, s)
	}
	if len(p.recovery) != 1 || p.pliMerged.Load() != 0 {
		t.Fatal("slow viewer continuously requested publisher recovery")
	}
	<-p.recovery
	close(s.closed)
	requestRecoveryKeyframe(p, s)
	if len(p.recovery) != 0 {
		t.Fatal("closed viewer requested recovery")
	}
}

func awaitPLI(t *testing.T, sent <-chan time.Time) time.Time {
	t.Helper()
	select {
	case at := <-sent:
		return at
	case <-time.After(time.Second):
		t.Fatal("pending PLI was never sent")
		return time.Time{}
	}
}
