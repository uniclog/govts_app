package client

import (
	"context"
	"math"
	"time"

	"uniclog.io/sonoryx/internal/audio"
)

const DefaultJitterDepth = 3

// maxConcealedFrames bounds how many lost frames in one gap are replaced with
// Missing placeholders. Longer gaps end playout until the next real frame
// instead of synthesizing speech indefinitely.
const maxConcealedFrames = 3

const (
	DefaultStreamIdleTimeout = 30 * time.Second
	streamCleanupInterval    = 5 * time.Second
)

type jitterBufferStats struct {
	Lost       uint64
	Concealed  uint64
	Duplicates uint64
	TooOld     uint64
}

type jitterBuffer struct {
	depth       int
	initialized bool
	started     bool
	waitTicks   int
	next        uint32
	pending     map[uint32]audio.MediaFrame
	stats       jitterBufferStats
	// onGap, when set, observes the length of every confirmed gap.
	onGap func(length uint32)
}

func newJitterBuffer(depth int) *jitterBuffer {
	if depth < 1 {
		depth = 1
	}
	return &jitterBuffer{
		depth:   depth,
		pending: make(map[uint32]audio.MediaFrame),
	}
}

func (b *jitterBuffer) Push(frame audio.MediaFrame) []audio.MediaFrame {
	if !b.initialized {
		b.initialized = true
		b.next = frame.Sequence
	} else if b.started && sequenceBefore(frame.Sequence, b.next) {
		b.stats.TooOld++
		return nil
	}

	if _, exists := b.pending[frame.Sequence]; exists {
		b.stats.Duplicates++
		return nil
	}
	b.pending[frame.Sequence] = frame

	if !b.started {
		if sequenceBefore(frame.Sequence, b.next) {
			b.next = frame.Sequence
		}
		if len(b.pending) < b.depth {
			return nil
		}
		b.started = true
	}

	ready := b.release(false)
	if len(ready) > 0 {
		b.waitTicks = 0
	}
	return ready
}

func (b *jitterBuffer) Tick() []audio.MediaFrame {
	if len(b.pending) == 0 {
		b.waitTicks = 0
		return nil
	}

	b.waitTicks++
	if b.waitTicks < b.depth {
		return nil
	}

	b.waitTicks = 0
	b.started = true
	return b.release(true)
}

func (b *jitterBuffer) Flush() []audio.MediaFrame {
	if !b.initialized {
		return nil
	}
	b.started = true
	return b.release(true)
}

func (b *jitterBuffer) Stats() jitterBufferStats {
	return b.stats
}

func (b *jitterBuffer) release(flush bool) []audio.MediaFrame {
	var ready []audio.MediaFrame
	for len(b.pending) > 0 {
		if frame, ok := b.pending[b.next]; ok {
			ready = append(ready, frame)
			delete(b.pending, b.next)
			b.next++
			continue
		}

		if !flush && len(b.pending) < b.depth {
			break
		}

		sequence, distance := b.nearestPending()
		b.stats.Lost += uint64(distance)
		if b.onGap != nil {
			b.onGap(distance)
		}
		ready = b.appendMissing(ready, b.pending[sequence], distance)
		b.next = sequence
	}
	return ready
}

// appendMissing emits placeholders for the first frames of a confirmed gap so
// the decoder can conceal them and the stream keeps one frame per interval.
func (b *jitterBuffer) appendMissing(
	ready []audio.MediaFrame,
	following audio.MediaFrame,
	distance uint32,
) []audio.MediaFrame {
	count := min(distance, maxConcealedFrames)
	for i := uint32(0); i < count; i++ {
		ready = append(ready, audio.MediaFrame{
			SenderID: following.SenderID,
			Sequence: b.next + i,
			Duration: following.Duration,
			Missing:  true,
		})
	}
	b.stats.Concealed += uint64(count)
	return ready
}

func (b *jitterBuffer) nearestPending() (uint32, uint32) {
	nearest := uint32(0)
	distance := uint32(math.MaxUint32)
	for sequence := range b.pending {
		candidateDistance := sequence - b.next
		if candidateDistance < distance {
			nearest = sequence
			distance = candidateDistance
		}
	}
	return nearest, distance
}

func sequenceBefore(left uint32, right uint32) bool {
	// Converting the unsigned distance to int32 also keeps comparison valid
	// when uint32 wraps from MaxUint32 back to zero.
	return int32(left-right) < 0
}

type streamJitterBuffers struct {
	depth    int
	bySender map[uint64]jitterStream
	onGap    func(length uint32)
}

type jitterStream struct {
	buffer   *jitterBuffer
	lastSeen time.Time
}

func newStreamJitterBuffers(depth int) *streamJitterBuffers {
	return &streamJitterBuffers{
		depth:    depth,
		bySender: make(map[uint64]jitterStream),
	}
}

func (b *streamJitterBuffers) Push(frame audio.MediaFrame) []audio.MediaFrame {
	return b.PushAt(frame, time.Now())
}

func (b *streamJitterBuffers) PushAt(
	frame audio.MediaFrame,
	now time.Time,
) []audio.MediaFrame {
	stream, ok := b.bySender[frame.SenderID]
	if !ok {
		stream.buffer = newJitterBuffer(b.depth)
		stream.buffer.onGap = b.onGap
	}
	stream.lastSeen = now
	b.bySender[frame.SenderID] = stream
	return stream.buffer.Push(frame)
}

func (b *streamJitterBuffers) Flush() []audio.MediaFrame {
	var ready []audio.MediaFrame
	for _, stream := range b.bySender {
		ready = append(ready, stream.buffer.Flush()...)
	}
	return ready
}

func (b *streamJitterBuffers) Tick() []audio.MediaFrame {
	var ready []audio.MediaFrame
	for _, stream := range b.bySender {
		ready = append(ready, stream.buffer.Tick()...)
	}
	return ready
}

func (b *streamJitterBuffers) RemoveInactive(now time.Time, timeout time.Duration) int {
	removed := 0
	for senderID, stream := range b.bySender {
		if now.Sub(stream.lastSeen) < timeout {
			continue
		}
		delete(b.bySender, senderID)
		removed++
	}
	return removed
}

func JitterLoop(
	ctx context.Context,
	mediaCh <-chan audio.MediaFrame,
	orderedCh chan<- audio.MediaFrame,
	depth int,
	states ...*State,
) error {
	defer close(orderedCh)
	buffers := newStreamJitterBuffers(depth)
	if len(states) > 0 {
		buffers.onGap = states[0].RecordVoiceLossBurst
	}
	ticker := time.NewTicker(frameDuration)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C:
			buffers.RemoveInactive(now, DefaultStreamIdleTimeout)
			if err := sendOrderedFrames(ctx, orderedCh, buffers.Tick()); err != nil {
				return err
			}
		case frame, ok := <-mediaCh:
			if !ok {
				return sendOrderedFrames(ctx, orderedCh, buffers.Flush())
			}
			if err := sendOrderedFrames(ctx, orderedCh, buffers.Push(frame)); err != nil {
				return err
			}
		}
	}
}

func sendOrderedFrames(
	ctx context.Context,
	orderedCh chan<- audio.MediaFrame,
	frames []audio.MediaFrame,
) error {
	for _, frame := range frames {
		select {
		case orderedCh <- frame:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}
