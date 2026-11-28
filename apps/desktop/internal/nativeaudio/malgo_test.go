package nativeaudio

import (
	"encoding/binary"
	"math"
	"testing"
	"time"
	"uniclog.io/sonoryx/internal/audio"
)

// deliver feeds the recorder like the device callback does and drains the
// queue like a consumer that keeps up, so no chunk is dropped.
func deliver(r *MalgoRecorder, chunks int, framesPerChunk uint32) {
	input := make([]byte, framesPerChunk*2)
	for i := 0; i < chunks; i++ {
		r.push(input, framesPerChunk)
		<-r.dataCh
	}
}

func TestCaptureRateCheck(t *testing.T) {
	const window = captureRateWindow
	tests := []struct {
		name        string
		chunks      int
		frames      uint32
		elapsed     time.Duration
		wantReport  bool
		wantRate    float64
		wantDropped uint64
	}{
		{"real time", 250, 960, window, false, 48000, 0},
		{"within tolerance", 260, 960, window, false, 49920, 0},
		{"surplus sounds slowed", 357, 960, window, true, 68544, 0},
		{"deficit", 225, 960, window, true, 43200, 0},
		{"idle reader is ignored", 0, 0, captureRateIdle + time.Second, false, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newMalgoRecorder(audio.CodecConfig{SampleRate: 48000})
			start := time.Unix(1000, 0)
			if _, ok := r.checkCaptureRate(start); ok {
				t.Fatal("first check reported an anomaly")
			}
			deliver(r, tt.chunks, tt.frames)
			report, ok := r.checkCaptureRate(start.Add(tt.elapsed))
			if ok != tt.wantReport {
				t.Fatalf("reported = %t, want %t (report %+v)", ok, tt.wantReport, report)
			}
			if ok && math.Abs(report.framesPerSecond-tt.wantRate) > 1 {
				t.Fatalf("frames_per_s = %.1f, want %.1f", report.framesPerSecond, tt.wantRate)
			}
			if report.dropped != tt.wantDropped {
				t.Fatalf("dropped = %d, want %d", report.dropped, tt.wantDropped)
			}
			if r.callbackFrames.Load() != 0 || r.droppedChunks.Load() != 0 {
				t.Fatal("counters were not reset after the window")
			}
		})
	}
}

func TestCaptureRateReportsDroppedChunks(t *testing.T) {
	r := newMalgoRecorder(audio.CodecConfig{SampleRate: 48000})
	start := time.Unix(1000, 0)
	r.checkCaptureRate(start)
	// The consumer stalls: the queue fills and the next chunk is dropped.
	input := make([]byte, 960*2)
	for i := 0; i < captureQueueChunks+1; i++ {
		r.push(input, 960)
	}
	for len(r.dataCh) > 0 {
		<-r.dataCh
	}
	deliver(r, 250-captureQueueChunks-1, 960)
	report, ok := r.checkCaptureRate(start.Add(captureRateWindow))
	if !ok || report.dropped != 1 {
		t.Fatalf("report = %+v, reported = %t; want one dropped chunk", report, ok)
	}
	if math.Abs(report.framesPerSecond-48000) > 1 {
		t.Fatalf("frames_per_s = %.1f, want 48000", report.framesPerSecond)
	}
}

func TestCaptureRateKeepsCountingInsideWindow(t *testing.T) {
	r := newMalgoRecorder(audio.CodecConfig{SampleRate: 48000})
	start := time.Unix(1000, 0)
	r.checkCaptureRate(start)
	deliver(r, 100, 960)
	if _, ok := r.checkCaptureRate(start.Add(captureRateWindow / 2)); ok {
		t.Fatal("reported before the window finished")
	}
	deliver(r, 150, 960)
	if report, ok := r.checkCaptureRate(start.Add(captureRateWindow)); ok {
		t.Fatalf("frames from both halves did not add up: %+v", report)
	}
}

func TestMalgoRecorderPushCopiesInput(t *testing.T) {
	r := newMalgoRecorder(audio.CodecConfig{SampleRate: 48000})
	input := []byte{1, 2}
	r.push(input, 1)
	input[0] = 9
	if chunk := <-r.dataCh; chunk[0] != 1 {
		t.Fatal("callback buffer was not copied")
	}
	r.push(nil, 0)
	if len(r.dataCh) != 0 || r.callbackFrames.Load() != 1 {
		t.Fatal("empty callback was queued or counted")
	}
}

func TestMalgoRecorderReadJoinsChunks(t *testing.T) {
	r := newMalgoRecorder(audio.CodecConfig{SampleRate: 48000})
	want := []int16{1, -2, 300, -32768, 32767}
	encoded := make([]byte, len(want)*2)
	for i, sample := range want {
		binary.LittleEndian.PutUint16(encoded[i*2:], uint16(sample))
	}
	r.push(encoded[:4], 2)
	r.push(encoded[4:], 3)

	got := make([]int16, len(want))
	n, err := r.Read(got)
	if err != nil || n != len(want) {
		t.Fatalf("Read = %d, %v", n, err)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d = %d, want %d", i, got[i], want[i])
		}
	}
}
