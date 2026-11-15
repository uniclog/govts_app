package audio

import (
	"sync"
	"testing"
	"time"
)

const framesPer20ms = 960

// fakeClock replaces time.Now and time.Sleep: sleeping advances the clock
// instead of waiting, so pacing is checked without real time.
type fakeClock struct {
	mu    sync.Mutex
	at    time.Time
	slept time.Duration
}

func newFakeClock() *fakeClock { return &fakeClock{at: time.Unix(1000, 0)} }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) sleep(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
	c.slept += d
}

// advance moves the clock without counting it as pacing.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func (c *fakeClock) sleptTotal() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.slept
}

// exclusiveRecorder is a recorder that opened an exclusive stream, on a fake clock.
func exclusiveRecorder(bufferMS int) (*MalgoRecorder, *fakeClock) {
	clock := newFakeClock()
	r := newMalgoRecorder(CodecConfig{SampleRate: 48000})
	r.now, r.sleep = clock.now, clock.sleep
	r.exclusiveMS = bufferMS
	r.queueLimit.Store(exclusiveQueueChunks(bufferMS))
	return r, clock
}

// pushChunks queues chunks of 20 ms at the current fake time.
func pushChunks(r *MalgoRecorder, chunks int) {
	input := make([]byte, framesPer20ms*2)
	for i := 0; i < chunks; i++ {
		r.push(input, framesPer20ms)
	}
}

func TestObserveDeliveryKeepsLongestGap(t *testing.T) {
	r, _ := exclusiveRecorder(100)
	start := time.Unix(1000, 0)
	for _, at := range []time.Duration{0, 1 * ms, 100 * ms, 230 * ms, 231 * ms, 300 * ms} {
		r.observeDelivery(start.Add(at))
	}
	if got := time.Duration(r.maxGapNS.Load()); got != 130*ms {
		t.Fatalf("longest gap = %s, want 130ms", got)
	}
}

// adaptWindow runs one finished adaptation window with full audio and the
// given callback times, relative to the window start.
func adaptWindow(r *MalgoRecorder, clock *fakeClock, callbacks ...time.Duration) {
	start := clock.now()
	r.adaptBuffer(start)
	for _, at := range callbacks {
		r.observeDelivery(start.Add(at))
	}
	r.adaptFrames.Store(5 * 48000)
	r.adaptBuffer(start.Add(captureRateWindow))
}

func TestAdaptBufferLatenessIsGapBeyondBuffer(t *testing.T) {
	tests := []struct {
		name      string
		callbacks []time.Duration
		want      int
	}{
		// One event per 100 ms buffer, delivered as five back-to-back chunks.
		{"on time", []time.Duration{0, 100 * ms, 200 * ms, 300 * ms}, 100},
		// Chunks of one delivery split by preemption only shorten the next gap.
		{"split delivery", []time.Duration{0, 15 * ms, 30 * ms, 100 * ms, 200 * ms}, 100},
		// A 170 ms gap is 70 ms late: twice that is 140 ms.
		{"late wake-up", []time.Duration{0, 100 * ms, 270 * ms, 300 * ms}, 140},
		{"within the reserve", []time.Duration{0, 150 * ms}, 100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setBufferState(t, 100)
			r, clock := exclusiveRecorder(100)
			adaptWindow(r, clock, tt.callbacks...)
			if ExclusiveCaptureBufferMS() != tt.want {
				t.Fatalf("buffer = %d ms, want %d", ExclusiveCaptureBufferMS(), tt.want)
			}
		})
	}
}

func TestAdaptBufferWaitsForWindowAndResetsCounters(t *testing.T) {
	start := time.Unix(1000, 0)
	setBufferState(t, 100)
	r, _ := exclusiveRecorder(100)
	r.adaptBuffer(start)

	r.adaptFrames.Store(5 * 48000)
	r.maxGapNS.Store(int64(170 * ms))
	r.adaptBuffer(start.Add(captureRateWindow / 2))
	if ExclusiveCaptureBufferMS() != 100 {
		t.Fatal("adapted before the window finished")
	}
	r.adaptBuffer(start.Add(captureRateWindow))
	if ExclusiveCaptureBufferMS() != 140 {
		t.Fatalf("buffer = %d ms, want 140", ExclusiveCaptureBufferMS())
	}
	if r.adaptFrames.Load() != 0 || r.maxGapNS.Load() != 0 || r.adaptDropped.Load() != 0 {
		t.Fatal("window counters were not reset")
	}
}

func TestAdaptBufferDiscardsStartupWindow(t *testing.T) {
	start := time.Unix(1000, 0)
	setBufferState(t, 100)
	r, _ := exclusiveRecorder(100)
	// A gap and drops from the device start-up, before the first window.
	r.maxGapNS.Store(int64(400 * ms))
	r.adaptDropped.Store(3)
	r.adaptBuffer(start)
	r.adaptFrames.Store(5 * 48000)
	r.adaptBuffer(start.Add(captureRateWindow))
	if ExclusiveCaptureBufferMS() != 100 {
		t.Fatalf("start-up changed the buffer to %d ms", ExclusiveCaptureBufferMS())
	}
}

func TestAdaptBufferDetectsLostAudio(t *testing.T) {
	tests := []struct {
		name    string
		frames  uint64
		dropped uint64
		want    int
	}{
		{"real time", 5 * 48000, 0, 100},
		// Delivery boundaries may move a window by one buffer.
		{"one buffer short", 5*48000 - 100*48, 0, 100},
		{"200 ms short", 5*48000 - 200*48, 0, 120},
		{"dropped chunk", 5 * 48000, 1, 120},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start := time.Unix(1000, 0)
			setBufferState(t, 100)
			r, _ := exclusiveRecorder(100)
			r.adaptBuffer(start)
			r.adaptFrames.Store(tt.frames)
			r.adaptDropped.Store(tt.dropped)
			r.adaptBuffer(start.Add(captureRateWindow))
			if ExclusiveCaptureBufferMS() != tt.want {
				t.Fatalf("buffer = %d ms, want %d", ExclusiveCaptureBufferMS(), tt.want)
			}
		})
	}
}

func TestAdaptBufferIgnoresIdleReaderAndSharedStream(t *testing.T) {
	start := time.Unix(1000, 0)
	setBufferState(t, 100)

	r, _ := exclusiveRecorder(100)
	r.adaptBuffer(start)
	r.adaptDropped.Store(5)
	r.adaptBuffer(start.Add(captureRateIdle + time.Second))
	if ExclusiveCaptureBufferMS() != 100 {
		t.Fatalf("idle window changed the buffer to %d ms", ExclusiveCaptureBufferMS())
	}

	shared := newMalgoRecorder(CodecConfig{SampleRate: 48000})
	shared.adaptBuffer(start)
	shared.adaptDropped.Store(5)
	shared.adaptBuffer(start.Add(captureRateWindow))
	if ExclusiveCaptureBufferMS() != 100 {
		t.Fatalf("shared stream changed the buffer to %d ms", ExclusiveCaptureBufferMS())
	}
}

func TestExclusiveQueueHoldsTwoBuffers(t *testing.T) {
	for _, bufferMS := range []int{ExclusiveCaptureBufferStartMS, 160, ExclusiveCaptureBufferMaxMS} {
		r, _ := exclusiveRecorder(bufferMS)
		chunks := 2 * bufferMS / ExclusiveCaptureBufferStepMS
		pushChunks(r, chunks)
		if r.droppedChunks.Load() != 0 || r.adaptDropped.Load() != 0 {
			t.Fatalf("%d ms: dropped chunks of two buffers", bufferMS)
		}
		if got := r.queuedFrames.Load(); got != int64(chunks*framesPer20ms) {
			t.Fatalf("%d ms: queued frames = %d, want %d", bufferMS, got, chunks*framesPer20ms)
		}
	}
	if exclusiveQueueChunks(1000) != captureQueueChunksMax {
		t.Fatal("queue limit exceeds the channel capacity")
	}
}

func TestQueueLimitDropsAndCountsForAdaptation(t *testing.T) {
	r, _ := exclusiveRecorder(ExclusiveCaptureBufferStartMS)
	limit := int(exclusiveQueueChunks(ExclusiveCaptureBufferStartMS))
	pushChunks(r, limit+2)
	if r.droppedChunks.Load() != 2 || r.adaptDropped.Load() != 2 || len(r.dataCh) != limit {
		t.Fatalf("dropped = %d/%d, queued = %d; want 2 drops at limit %d", r.droppedChunks.Load(), r.adaptDropped.Load(), len(r.dataCh), limit)
	}
	if got := r.queuedFrames.Load(); got != int64(limit*framesPer20ms) {
		t.Fatalf("queued frames = %d, want %d", got, limit*framesPer20ms)
	}
}

func TestReadUpdatesQueuedFrames(t *testing.T) {
	r, _ := exclusiveRecorder(100)
	pushChunks(r, 3)
	samples := make([]int16, framesPer20ms/2)
	if _, err := r.Read(samples); err != nil {
		t.Fatal(err)
	}
	// One chunk left the channel; half of it is still pending.
	if got := r.queuedFrames.Load(); got != 2*framesPer20ms {
		t.Fatalf("queued frames = %d, want %d", got, 2*framesPer20ms)
	}
}

// readFrames reads n 20 ms frames.
func readFrames(t *testing.T, r *MalgoRecorder, n int) {
	t.Helper()
	samples := make([]int16, framesPer20ms)
	for i := 0; i < n; i++ {
		if _, err := r.Read(samples); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPacingSpreadsChunkedBuffer(t *testing.T) {
	r, clock := exclusiveRecorder(100)
	// One 100 ms delivery split into a chunk per period.
	pushChunks(r, 5)
	readFrames(t, r, 5)
	// The first frame leaves at once, the other four one per 20 ms.
	if got := clock.sleptTotal(); got != 80*ms {
		t.Fatalf("paced for %s, want 80ms", got)
	}
}

func TestPacingCatchesUpWhenMoreThanBufferQueued(t *testing.T) {
	r, clock := exclusiveRecorder(100)
	// Two buffers queued: the capture thread woke late or the clock runs fast.
	pushChunks(r, 10)
	// Backlogs of 9, 8 and 7 frames exceed one buffer plus a frame (6), so
	// they leave at once; pacing resumes at 6.
	readFrames(t, r, 3)
	if got := clock.sleptTotal(); got != 0 {
		t.Fatalf("paced for %s while catching up, want 0", got)
	}
	readFrames(t, r, 1)
	if got := clock.sleptTotal(); got != 20*ms {
		t.Fatalf("paced for %s at one buffer of backlog, want 20ms", got)
	}
}

func TestPacingRestartsAfterReaderFellBehind(t *testing.T) {
	r, clock := exclusiveRecorder(100)
	pushChunks(r, 5)
	readFrames(t, r, 2)
	// The reader stalled longer than a frame: it must not wait for the old schedule.
	clock.advance(50 * ms)
	before := clock.sleptTotal()
	readFrames(t, r, 1)
	if got := clock.sleptTotal() - before; got != 0 {
		t.Fatalf("waited %s after falling behind, want 0", got)
	}
}

func TestSharedStreamIsNotPaced(t *testing.T) {
	clock := newFakeClock()
	r := newMalgoRecorder(CodecConfig{SampleRate: 48000})
	r.now, r.sleep = clock.now, clock.sleep
	pushChunks(r, 5)
	readFrames(t, r, 5)
	if got := clock.sleptTotal(); got != 0 {
		t.Fatalf("shared stream paced for %s, want 0", got)
	}
}

// bufferedDevice is an exclusive device that remembers the buffer it opened with.
type bufferedDevice struct {
	*exclusiveDevice
	bufferMS int
}

func (d *bufferedDevice) ExclusiveBufferMS() int { return d.bufferMS }

func TestSwitchableReopensWhenBufferGrows(t *testing.T) {
	setBufferState(t, ExclusiveCaptureBufferStartMS)
	jack := &endpoint{}
	var mu sync.Mutex
	var opened []int
	open := func(CodecConfig, string) (captureDevice, error) {
		device, err := jack.open(1)
		if err != nil {
			return nil, err
		}
		bufferMS := ExclusiveCaptureBufferMS()
		mu.Lock()
		opened = append(opened, bufferMS)
		mu.Unlock()
		return &bufferedDevice{exclusiveDevice: device.(*exclusiveDevice), bufferMS: bufferMS}, nil
	}
	recorder, err := newSwitchableRecorder(CodecConfig{}, "", open)
	if err != nil {
		t.Fatal(err)
	}
	defer recorder.Close()

	readOne(t, recorder)
	adaptExclusiveCaptureBuffer(ExclusiveCaptureBufferStartMS, 0, true)
	readOne(t, recorder)
	readOne(t, recorder)

	mu.Lock()
	defer mu.Unlock()
	if len(opened) != 2 || opened[0] != ExclusiveCaptureBufferStartMS || opened[1] != ExclusiveCaptureBufferStartMS+ExclusiveCaptureBufferStepMS {
		t.Fatalf("opened with %v ms, want [%d %d]", opened, ExclusiveCaptureBufferStartMS, ExclusiveCaptureBufferStartMS+ExclusiveCaptureBufferStepMS)
	}
	if !jack.held() {
		t.Fatal("reopened device does not hold the endpoint")
	}
}
