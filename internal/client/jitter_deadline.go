package client

import (
	"time"
	"uniclog.io/sonoryx/internal/audio"
)

// maxPlayoutExcess bounds how far the newest frame may sit beyond the target
// reserve. It covers the largest VAD pre-roll burst (100 ms) plus 60 ms of
// margin, so ordinary speech onsets stay intact.
const maxPlayoutExcess = 160 * time.Millisecond

const (
	minPlayoutReserve = 40 * time.Millisecond
	maxPlayoutReserve = 160 * time.Millisecond
)

// deadlineJitter is the live v2 playout clock. Packet arrival never drains it.
// A new talkspurt anchors its newest frame, not its oldest redundant copy.
type deadlineJitter struct {
	pending                   map[uint32]audio.MediaFrame
	anchor, lastNew, deadline time.Time
	goodActive                time.Duration
	anchorSequence, next      uint32
	reserve, target           time.Duration
	initialized               bool
	concealed                 int
	inGap                     bool
	prototype                 audio.MediaFrame
	onGap                     func(uint32)
	onMetric                  func(sequence, restored, late uint32)
}

func newDeadlineJitter() *deadlineJitter {
	return &deadlineJitter{pending: make(map[uint32]audio.MediaFrame), target: minPlayoutReserve}
}
func (d *deadlineJitter) due(sequence uint32) time.Time {
	return d.anchor.Add(time.Duration(int32(sequence-d.anchorSequence)) * frameDuration)
}
func (d *deadlineJitter) Push(frames []audio.MediaFrame, now time.Time) {
	if len(frames) == 0 {
		return
	}
	newest := frames[len(frames)-1]
	arrival := newest.ArrivedAt
	if arrival.IsZero() {
		arrival = now
	}
	activeStep := max(time.Duration(0), min(frameDuration, arrival.Sub(d.lastNew)))
	forward := !d.initialized || (int32(newest.Sequence-d.prototype.Sequence) > 0 && int32(newest.Sequence-d.next) >= 0)
	// Capture gating compresses sequence time, and capture/device clocks can
	// drift. Once playout is exhausted, a new overdue frame starts a fresh
	// bounded reserve instead of keeping the entire next talkspurt late.
	// Older repairs must never move the clock, even after a long silence.
	restart := !d.initialized || (forward && (arrival.Sub(d.lastNew) >= 200*time.Millisecond || (len(d.pending) == 0 && now.After(d.due(newest.Sequence)))))
	if restart {
		activeStep = 0
		clear(d.pending)
		d.reserve = d.target
		// Account for time spent waiting for this consumer as well as arrival.
		d.anchor = now.Add(d.reserve)
		d.anchorSequence = newest.Sequence
		d.next = frames[0].Sequence
		d.initialized = true
		d.concealed = 0
		d.inGap = false
		// The anchor is based on now, so skip the copies that insertion below
		// would reject as overdue; otherwise the cursor starts on a hole.
		for d.next != newest.Sequence && now.After(d.due(d.next)) {
			d.next++
		}
		d.deadline = d.due(d.next)
	}
	// A fresh primary can reveal bursty capture, network jitter or a stalled
	// consumer. Apply reserve growth to the running clock before rejecting its
	// frames. Old repairs must not delay speech or rewind the playout cursor.
	if !restart && forward && now.After(d.due(newest.Sequence)) {
		d.extendReserve(now.Sub(d.due(newest.Sequence)) + frameDuration)
	}
	if restart || int32(newest.Sequence-d.prototype.Sequence) > 0 {
		d.lastNew = arrival
		d.prototype = newest
	} else {
		activeStep = 0
	}
	for _, frame := range frames {
		if int32(frame.Sequence-d.next) < 0 || now.After(d.due(frame.Sequence)) {
			if d.onMetric != nil {
				d.onMetric(frame.DatagramSequence, 0, 1)
			}
			d.target = min(maxPlayoutReserve, d.target+frameDuration)
			d.goodActive = 0
			continue
		}
		if _, exists := d.pending[frame.Sequence]; exists {
			continue
		}
		if len(d.pending) >= 128 {
			continue
		}
		d.pending[frame.Sequence] = frame
		if frame.Redundant && d.onMetric != nil {
			d.onMetric(frame.DatagramSequence, 1, 0)
		}
	}
	// Measure depth from now: time already spent waiting for this consumer is
	// not queued latency and must not trim a talkspurt that was just anchored.
	d.trimDepth(newest.Sequence, now)
	d.goodActive += activeStep
	if d.goodActive >= 20*time.Second {
		d.target = max(minPlayoutReserve, d.target-frameDuration)
		d.goodActive = 0
	}

}

// extendReserve preserves queued speech across short stalls. Growth is shared
// by arrivals and ticks and bounded for the entire current talkspurt: repeated
// stalls cannot move the clock indefinitely. Reductions apply on restart.
func (d *deadlineJitter) extendReserve(delay time.Duration) {
	if delay <= 0 {
		return
	}
	delay = ((delay + frameDuration - 1) / frameDuration) * frameDuration
	delay = min(delay, maxPlayoutReserve-d.reserve)
	if delay > 0 {
		d.reserve += delay
		d.anchor = d.anchor.Add(delay)
		d.deadline = d.deadline.Add(delay)
		d.target = max(d.target, d.reserve)
	}
	d.goodActive = 0
}

// trimDepth keeps latency bounded when frames arrive far ahead of the fixed
// anchor. A sender's capture clock running fast does this gradually and would
// otherwise queue seconds of unbroken speech; an anchor taken from a delayed
// packet does it at once when the network recovers. The oldest frames are
// skipped and the anchor moves by the same amount, so d.deadline stays valid
// for the new d.next. Plain loss does not trigger this: the distance depends
// on the newest sequence and the time it is processed, not on the queue length.
func (d *deadlineJitter) trimDepth(newest uint32, now time.Time) {
	excess := d.due(newest).Sub(now) - d.target - maxPlayoutExcess
	if excess <= 0 {
		return
	}
	frames := uint32((excess + frameDuration - 1) / frameDuration)
	d.anchor = d.anchor.Add(-time.Duration(frames) * frameDuration)
	d.next += frames
	for sequence, frame := range d.pending {
		if int32(sequence-d.next) < 0 {
			delete(d.pending, sequence)
			if d.onMetric != nil {
				d.onMetric(frame.DatagramSequence, 0, 1)
			}
		}
	}
}

func (d *deadlineJitter) Tick(now time.Time) []audio.MediaFrame {
	if !d.initialized || len(d.pending) == 0 || now.Before(d.deadline) {
		return nil
	}
	// Absorb a short scheduling stall before skipping queued speech. Once the
	// reserve budget is exhausted, discard excess instead of emitting a batch.
	if behind := now.Sub(d.deadline) / frameDuration; behind > 0 {
		d.extendReserve(behind * frameDuration)
	}
	if behind := now.Sub(d.deadline) / frameDuration; behind > 0 {
		d.target = min(maxPlayoutReserve, d.target+frameDuration)
		d.goodActive = 0
		d.next += uint32(behind)
		d.deadline = d.deadline.Add(behind * frameDuration)
		for sequence := range d.pending {
			if int32(sequence-d.next) < 0 {
				frame := d.pending[sequence]
				delete(d.pending, sequence)
				if d.onMetric != nil {
					d.onMetric(frame.DatagramSequence, 0, 1)
				}
			}
		}
	}
	frame, exists := d.pending[d.next]
	if exists {
		delete(d.pending, d.next)
		if d.inGap && d.onGap != nil {
			d.onGap(uint32(d.concealed))
		}
		d.concealed = 0
		d.inGap = false
	} else {
		var nearest uint32
		found := false
		for seq := range d.pending {
			if int32(seq-d.next) > 0 && (!found || uint32(seq-d.next) < uint32(nearest-d.next)) {
				nearest = seq
				found = true
			}
		}
		if !found {
			return nil
		}
		if !d.inGap {
			d.inGap = true

		}
		d.concealed++
		frame = d.prototype
		frame.Sequence = d.next
		frame.Data = nil
		frame.Missing = true
		frame.Redundant = false
	}
	d.next++
	d.deadline = d.deadline.Add(frameDuration)
	if !exists && d.concealed > maxConcealedFrames {
		return nil
	}
	return []audio.MediaFrame{frame}
}
