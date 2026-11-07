//go:build darwin

package audio

import (
	"context"
	"encoding/binary"
	"math"
	"testing"
	"time"
)

func samples(pcm []byte) []int16 {
	out := make([]int16, len(pcm)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(pcm[2*i:]))
	}
	return out
}

func TestAppendSampleScalesAndClamps(t *testing.T) {
	for _, test := range []struct {
		in   float32
		want int16
	}{{0, 0}, {1, 32767}, {-1, -32767}, {0.5, 16384}, {2, 32767}, {-3, -32767}} {
		got := samples(appendSample(nil, test.in))[0]
		if got != test.want {
			t.Errorf("appendSample(%v) = %d, want %d", test.in, got, test.want)
		}
	}
}

func TestTapSinkPassesThroughAt48k(t *testing.T) {
	capture := newLoopbackCapture()
	sink := &tapSink{capture: capture, ratio: 1}
	sink.push([]float32{0.5, -0.5}, []float32{-1, 1})
	got := samples(capture.pending)
	want := []int16{16384, -32767, -16384, 32767}
	if len(got) != len(want) {
		t.Fatalf("got %d samples, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sample %d = %d, want %d (interleaved L,R)", i, got[i], want[i])
		}
	}
}

// A tap at 44.1 kHz must come out at 48 kHz without drift across callbacks.
func TestTapSinkResamplesToLoopbackRate(t *testing.T) {
	capture := newLoopbackCapture()
	sink := &tapSink{capture: capture, ratio: 1}
	sink.setRate(44100)
	const callbacks, frames = 100, 441 // one second of 44.1 kHz
	left, right := make([]float32, frames), make([]float32, frames)
	produced := 0
	for c := 0; c < callbacks; c++ {
		for i := range left {
			phase := 2 * math.Pi * 1000 * float64(c*frames+i) / 44100
			left[i], right[i] = float32(math.Sin(phase)), 0
		}
		capture.mu.Lock()
		capture.pending = capture.pending[:0]
		capture.mu.Unlock()
		sink.push(left, right)
		produced += len(capture.pending) / loopbackFrameBytes
	}
	if math.Abs(float64(produced-LoopbackSampleRate)) > 2 {
		t.Fatalf("one second at 44.1 kHz produced %d frames, want ~%d", produced, LoopbackSampleRate)
	}
}

func TestTapSinkInterpolatesBetweenCallbacks(t *testing.T) {
	capture := newLoopbackCapture()
	sink := &tapSink{capture: capture, ratio: 0.5} // upsample x2
	sink.push([]float32{0, 1}, []float32{0, 1})
	sink.push([]float32{0}, []float32{0})
	got := samples(capture.pending)
	var left []int16
	for i := 0; i < len(got); i += 2 {
		left = append(left, got[i])
	}
	// The source is x0 (primed copy), 0, 1 | 0. Output steps by half a frame
	// and interpolates across the seam between callbacks: 0, 0, 0, 0.5 from
	// the first call, then 1 and 0.5 between its last frame and the next.
	want := []int16{0, 0, 0, 16384, 32767, 16384}
	if len(left) != len(want) {
		t.Fatalf("left = %v, want %v", left, want)
	}
	for i := range want {
		if left[i] != want[i] {
			t.Fatalf("left = %v, want %v", left, want)
		}
	}
}

func TestLoopbackCaptureReadAfterPush(t *testing.T) {
	capture := newLoopbackCapture()
	sink := &tapSink{capture: capture, ratio: 1}
	frames := LoopbackSampleRate / 50 // 20 ms
	sink.push(make([]float32, frames), make([]float32, frames))
	pcm, err := capture.Read(context.Background(), 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if len(pcm) != frames*loopbackFrameBytes {
		t.Fatalf("read %d bytes, want %d", len(pcm), frames*loopbackFrameBytes)
	}
}

func TestLoopbackAvailableSkipsProbe(t *testing.T) {
	if loopbackAvailable == nil {
		t.Fatal("darwin must answer LoopbackSupported without a test capture")
	}
}

func TestTapSinkWarnsOnlyAfterSustainedDigitalSilence(t *testing.T) {
	sink := &tapSink{capture: newLoopbackCapture(), ratio: 1}
	zeros := make([]float32, LoopbackSampleRate) // one second
	for i := 0; i < 9; i++ {
		sink.push(zeros, zeros)
		sink.capture.pending = sink.capture.pending[:0]
	}
	if sink.warned {
		t.Fatal("warned before 10 s of silence")
	}
	sink.push(zeros, zeros)
	if !sink.warned {
		t.Fatal("no warning after 10 s of exact zeros")
	}

	heard := &tapSink{capture: newLoopbackCapture(), ratio: 1}
	heard.push([]float32{0, 0.25}, []float32{0, 0})
	for i := 0; i < 11; i++ {
		heard.push(zeros, zeros)
		heard.capture.pending = heard.capture.pending[:0]
	}
	if heard.warned {
		t.Fatal("warned although the tap had delivered sound")
	}
}
