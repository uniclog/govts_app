//go:build darwin

package audio

/*
#cgo CFLAGS: -fobjc-arc
#cgo LDFLAGS: -framework CoreAudio -framework AppKit -framework Foundation
#include <stdlib.h>
#include "loopback_darwin.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"log"
	"math"
	"runtime/cgo"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// Core Audio process taps (macOS 14.2+): a private tap of every process
// except Sonoryx, or of one app with its helper processes, read through a
// private aggregate device. macOS asks for "System Audio Recording" access
// on the first capture; the .app bundle carries NSAudioCaptureUsageDescription.

func init() { loopbackAvailable = func() bool { return C.srxTapAvailable() != 0 } }

// tapSink turns the tap's float stereo at the device rate into the 48 kHz
// s16le stereo the WebView expects.
type tapSink struct {
	capture *LoopbackCapture
	ratio   float64 // source frames per output frame
	pos     float64 // position of the next output frame, relative to prevL/R
	prevL   float32
	prevR   float32
	primed  bool
	out     []byte
	// left and right hold deinterleaved callback frames, reused so the IO
	// proc does not allocate on every 10 ms buffer.
	left, right []float32
	// silent counts output frames since start that were all exact zero.
	silent int
	heard  bool
	warned bool
}

// silenceWarning is how long a tap may deliver nothing but digital zero
// before the log names the likely cause. macOS gives a tap silence, not an
// error, when System Audio Recording access is missing.
const silenceWarning = 10 * LoopbackSampleRate

func (s *tapSink) push(left, right []float32) {
	s.resample(left, right)
}

// trackSilence logs once when the tap has produced only zeros for
// silenceWarning frames. Nothing playing is rarely exact digital zero for
// that long, so the first non-zero sample ends the check.
func (s *tapSink) trackSilence(pcm []byte) {
	if s.heard || s.warned {
		return
	}
	for _, b := range pcm {
		if b != 0 {
			s.heard = true
			return
		}
	}
	s.silent += len(pcm) / loopbackFrameBytes
	if s.silent >= silenceWarning {
		s.warned = true
		log.Printf("screen audio: process tap returned only silence for 10 s; if sound is playing, allow Sonoryx in System Settings → Privacy & Security → Screen & System Audio Recording")
	}
}

func (s *tapSink) resample(left, right []float32) {
	if s.ratio == 1 {
		s.out = s.out[:0]
		for i := range left {
			s.out = appendSample(appendSample(s.out, left[i]), right[i])
		}
		s.trackSilence(s.out)
		s.capture.push(s.out)
		return
	}
	// Linear interpolation between the last frame of the previous callback
	// (index 0) and this callback's frames (index 1..n).
	s.out = s.out[:0]
	at := func(i int, ch []float32, prev float32) float32 {
		if i == 0 {
			return prev
		}
		return ch[i-1]
	}
	n := len(left)
	if !s.primed && n > 0 {
		s.prevL, s.prevR, s.primed = left[0], right[0], true
	}
	for s.pos < float64(n) {
		i := int(s.pos)
		frac := float32(s.pos - float64(i))
		l0, l1 := at(i, left, s.prevL), at(i+1, left, s.prevL)
		r0, r1 := at(i, right, s.prevR), at(i+1, right, s.prevR)
		s.out = appendSample(appendSample(s.out, l0+(l1-l0)*frac), r0+(r1-r0)*frac)
		s.pos += s.ratio
	}
	s.pos -= float64(n)
	if n > 0 {
		s.prevL, s.prevR = left[n-1], right[n-1]
	}
	if len(s.out) > 0 {
		s.trackSilence(s.out)
		s.capture.push(s.out)
	}
}

func appendSample(out []byte, v float32) []byte {
	if v > 1 {
		v = 1
	} else if v < -1 {
		v = -1
	}
	s := int16(math.Round(float64(v) * 32767))
	return append(out, byte(s), byte(uint16(s)>>8))
}

// srxLoopbackRate runs before the IO proc starts, so push sees the ratio.
//
//export srxLoopbackRate
func srxLoopbackRate(handle C.uintptr_t, rate C.double) {
	cgo.Handle(handle).Value().(*tapSink).setRate(float64(rate))
}

func (s *tapSink) setRate(rate float64) {
	if rate > 0 {
		s.ratio = rate / LoopbackSampleRate
	}
}

//export srxLoopbackPush
func srxLoopbackPush(handle C.uintptr_t, left, right *C.float, frames, stride C.int) {
	if frames <= 0 {
		return
	}
	sink := cgo.Handle(handle).Value().(*tapSink)
	n, step := int(frames), int(stride)
	l := unsafe.Slice((*float32)(unsafe.Pointer(left)), (n-1)*step+1)
	r := unsafe.Slice((*float32)(unsafe.Pointer(right)), (n-1)*step+1)
	if step == 1 {
		sink.push(l, r)
		return
	}
	if cap(sink.left) < n {
		sink.left, sink.right = make([]float32, n), make([]float32, n)
	}
	ls, rs := sink.left[:n], sink.right[:n]
	for i := 0; i < n; i++ {
		ls[i], rs[i] = l[i*step], r[i*step]
	}
	sink.push(ls, rs)
}

// tapRun is one capture: the running tap and what is needed to rebuild it
// when the default output changes.
type tapRun struct {
	mu        sync.Mutex
	capture   *LoopbackCapture
	handle    cgo.Handle // the sink, passed to the IO proc
	pid       C.int32_t
	exclude   C.int
	tap       C.uint32_t
	aggregate C.uint32_t
	proc      unsafe.Pointer
	watch     unsafe.Pointer
	stopped   bool
	restart   *time.Timer
}

// tapRuns finds a run from the output listener by id. The listener can still
// fire while a run stops, so it never holds a pointer of its own.
var (
	tapRunsMu sync.Mutex
	tapRuns   = map[uintptr]*tapRun{}
	tapNextID uintptr
)

// outputRestartDelay coalesces the bursts of change notifications macOS sends
// while an output (Bluetooth headphones, for one) comes up.
const outputRestartDelay = 300 * time.Millisecond

//export srxLoopbackOutputChanged
func srxLoopbackOutputChanged(id C.uintptr_t) {
	tapRunsMu.Lock()
	run := tapRuns[uintptr(id)]
	tapRunsMu.Unlock()
	if run == nil {
		return
	}
	// Never block the CoreAudio listener queue: stop removes this listener
	// while holding run.mu.
	go run.scheduleRebuild()
}

func (run *tapRun) scheduleRebuild() {
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.stopped {
		return
	}
	if run.restart != nil {
		run.restart.Stop()
	}
	run.restart = time.AfterFunc(outputRestartDelay, run.rebuild)
}

func (run *tapRun) start() error {
	var result C.srxTapResult
	C.srxTapStart(run.pid, run.exclude, C.uintptr_t(run.handle), &result)
	if result.status != 0 {
		if result.status == -4 {
			return ErrLoopbackUnsupported
		}
		return fmt.Errorf("start process tap: %s", C.GoString(&result.message[0]))
	}
	run.tap, run.aggregate, run.proc = result.tap, result.aggregate, result.proc
	return nil
}

// rebuild moves the tap onto the new default output. The capture, its
// buffer and the reader stay; a failure ends the capture with the error.
func (run *tapRun) rebuild() {
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.stopped {
		return
	}
	C.srxTapStop(run.tap, run.aggregate, run.proc)
	run.tap, run.aggregate, run.proc = 0, 0, nil
	if err := run.start(); err != nil {
		log.Printf("screen audio restart after output change failed: %v", err)
		run.stopLocked(err)
		return
	}
	log.Printf("screen audio moved to the new default output")
}

func (run *tapRun) stopLocked(err error) {
	if run.stopped {
		return
	}
	run.stopped = true
	if run.restart != nil {
		run.restart.Stop()
	}
	tapRunsMu.Lock()
	for id, other := range tapRuns {
		if other == run {
			delete(tapRuns, id)
		}
	}
	tapRunsMu.Unlock()
	C.srxUnwatchDefaultOutput(run.watch)
	// AudioDeviceStop waits for a running IO proc, so the handle is no
	// longer used once it returns.
	C.srxTapStop(run.tap, run.aggregate, run.proc)
	run.handle.Delete()
	run.capture.finish(err)
}

// StartLoopback starts a process tap and returns once its device runs.
func StartLoopback(target LoopbackTarget) (*LoopbackCapture, error) {
	if C.srxTapAvailable() == 0 {
		return nil, ErrLoopbackUnsupported
	}
	capture := newLoopbackCapture()
	run := &tapRun{capture: capture, pid: C.int32_t(target.ProcessID)}
	run.handle = cgo.NewHandle(&tapSink{capture: capture, ratio: 1})
	if target.Exclude {
		run.exclude = 1
	}
	if err := run.start(); err != nil {
		run.handle.Delete()
		return nil, err
	}
	tapRunsMu.Lock()
	tapNextID++
	id := tapNextID
	tapRuns[id] = run
	tapRunsMu.Unlock()
	run.mu.Lock()
	run.watch = C.srxWatchDefaultOutput(C.uintptr_t(id))
	run.mu.Unlock()
	if run.watch == nil {
		log.Printf("screen audio: default output listener unavailable; an output change will stop the sound")
	}
	capture.stop = func() {
		run.mu.Lock()
		defer run.mu.Unlock()
		run.stopLocked(nil)
	}
	return capture, nil
}

// LoopbackApps lists regular (Dock) applications except Sonoryx.
func LoopbackApps() ([]LoopbackApp, error) {
	const capacity = 256
	buffer := (*[capacity]C.srxApp)(C.calloc(capacity, C.size_t(unsafe.Sizeof(C.srxApp{}))))
	if buffer == nil {
		return nil, errors.New("allocate application list")
	}
	defer C.free(unsafe.Pointer(buffer))
	count := int(C.srxListApps(&buffer[0], capacity))
	apps := make([]LoopbackApp, 0, count)
	for i := 0; i < count; i++ {
		name := C.GoString(&buffer[i].name[0])
		apps = append(apps, LoopbackApp{ProcessID: uint32(buffer[i].pid), Name: name, Title: name})
	}
	sort.Slice(apps, func(i, j int) bool { return strings.ToLower(apps[i].Title) < strings.ToLower(apps[j].Title) })
	return apps, nil
}
