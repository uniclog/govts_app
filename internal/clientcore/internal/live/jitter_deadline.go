package client

import (
	"time"
	"uniclog.io/sonoryx/internal/audio"
)

// maxPlayoutExcess bounds how far the newest frame may sit beyond the target
// reserve. It covers the largest VAD pre-roll burst (100 ms) plus 60 ms of
// margin, so ordinary speech onsets stay intact.
const maxPlayoutExcess = 160 * time.Millisecond

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
	return &deadlineJitter{pending: make(map[uint32]audio.MediaFrame), target: 40 * time.Millisecond}
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
		d.anchor = arrival.Add(d.reserve)
		d.anchorSequence = newest.Sequence
		d.next = frames[0].Sequence
		d.initialized = true
		d.concealed = 0
		d.inGap = false
		for d.next != newest.Sequence && d.due(d.next).Before(arrival) {
			d.next++
		}
		d.deadline = d.due(d.next)
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
			d.target = min(80*time.Millisecond, d.target+frameDuration)
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
	d.trimDepth(newest.Sequence, arrival)
	d.goodActive += activeStep
	if d.goodActive >= 20*time.Second {
		d.target = max(40*time.Millisecond, d.target-frameDuration)
		d.goodActive = 0
	}

}

// trimDepth keeps latency bounded when frames arrive far ahead of the fixed
// anchor. A sender's capture clock running fast does this gradually and would
// otherwise queue seconds of unbroken speech; an anchor taken from a delayed
// packet does it at once when the network recovers. The oldest frames are
// skipped and the anchor moves by the same amount, so d.deadline stays valid
// for the new d.next. Plain loss does not trigger this: the distance depends
// on the newest sequence and its arrival, not on the queue length.
func (d *deadlineJitter) trimDepth(newest uint32, arrival time.Time) {
	excess := d.due(newest).Sub(arrival) - d.target - maxPlayoutExcess
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
	// Discard overdue playout instead of catching up with an audible batch.
	if behind := now.Sub(d.deadline) / frameDuration; behind > 0 {
		d.target = min(80*time.Millisecond, d.target+frameDuration)
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
