package client

import (
	"context"
	"math"
	"time"

	"uniclog.io/sonoryx/internal/audio"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/voiceflow"
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
	return b.PushBatch([]audio.MediaFrame{frame})
}

// Insert every copy before deciding whether a gap requires PLC.
func (b *jitterBuffer) PushBatch(frames []audio.MediaFrame) []audio.MediaFrame {
	for _, frame := range frames {
		if !b.initialized {
			b.initialized = true
			b.next = frame.Sequence
		} else if b.started && sequenceBefore(frame.Sequence, b.next) {
			b.stats.TooOld++
			continue
		}
		if _, exists := b.pending[frame.Sequence]; exists {
			b.stats.Duplicates++
			continue
		}
		// Wire input cannot create an unbounded pending map.
		if frame.Voice.Generation != 0 && len(b.pending) >= 128 {
			continue
		}
		b.pending[frame.Sequence] = frame
		if !b.started && sequenceBefore(frame.Sequence, b.next) {
			b.next = frame.Sequence
		}
	}
	if !b.started {
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
			Voice:    following.Voice,
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
	pruned     voiceMediaStamp
	depth      int
	bySender   map[uint64]jitterStream
	onGap      func(length uint32)
	onMetric   func(sequence, restored, late uint32)
	onProtocol func()
}

type jitterStream struct {
	clock    *deadlineJitter
	identity audio.VoiceIdentity
	history  *voiceflow.Window
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
	frames := frame.Batch
	if len(frames) == 0 {
		frames = []audio.MediaFrame{frame}
	}
	frame = frames[0]
	stream, ok := b.bySender[frame.SenderID]
	if !ok || stream.identity != frame.Voice {
		stream.identity = frame.Voice
		stream.history = &voiceflow.Window{}
		stream.buffer = newJitterBuffer(b.depth)
		stream.buffer.onGap = b.onGap
		if frame.Voice.Generation != 0 {
			stream.clock = newDeadlineJitter()
			stream.clock.onGap = b.onGap
			stream.clock.onMetric = b.onMetric
		}
	}
	if frame.Voice.Generation != 0 {
		wire := make([]protocol.VoiceBundleFrame, 0, len(frames))
		for _, f := range frames {
			wire = append(wire, protocol.VoiceBundleFrame{Sequence: f.Sequence, Payload: f.Data})
		}
		lateBefore := stream.history.Late
		added, err := stream.history.Accept(wire, now)
		if err != nil {
			if b.onProtocol != nil {
				b.onProtocol()
			}
			return nil
		}
		originals := frames
		if b.onMetric != nil && stream.history.Late > lateBefore {
			b.onMetric(frame.DatagramSequence, 0, uint32(stream.history.Late-lateBefore))
		}
		frames = nil
		for _, f := range added {
			copy := frame
			for _, original := range originals {
				if original.Sequence == f.Sequence {
					copy = original
					break
				}
			}
			copy.Sequence = f.Sequence
			copy.Data = f.Payload
			frames = append(frames, copy)
		}
	}
	stream.lastSeen = now
	b.bySender[frame.SenderID] = stream
	if stream.clock != nil {
		stream.clock.Push(frames, now)
		return nil
	}
	return stream.buffer.PushBatch(frames)
}

func (b *streamJitterBuffers) Flush() []audio.MediaFrame {
	var ready []audio.MediaFrame
	for _, stream := range b.bySender {
		if stream.clock != nil {
			continue
		}
		ready = append(ready, stream.buffer.Flush()...)
	}
	return ready
}

func (b *streamJitterBuffers) Tick() []audio.MediaFrame {
	return b.TickAt(time.Now())
}
func (b *streamJitterBuffers) TickAt(now time.Time) []audio.MediaFrame {
	var ready []audio.MediaFrame
	for _, stream := range b.bySender {
		if stream.clock != nil {
			ready = append(ready, stream.clock.Tick(now)...)
			continue
		}
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

func (b *streamJitterBuffers) Prune(state *State) {
	if state == nil {
		return
	}
	stamp := state.mediaStamp()
	if stamp == b.pruned {
		return
	}
	b.pruned = stamp
	for id, stream := range b.bySender {
		if !state.VoiceMediaAllowed(id, stream.identity) {
			delete(b.bySender, id)
		}
	}
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
	var state *State
	var changes <-chan struct{}
	if len(states) > 0 {
		state = states[0]
		var unsubscribe func()
		changes, unsubscribe = state.Subscribe(ctx)
		defer unsubscribe()
	}
	if len(states) > 0 {
		buffers.onGap = states[0].RecordVoiceLossBurst
		endpoint := states[0].VoiceEndpoint()
		buffers.onProtocol = endpoint.ProtocolDrop
		buffers.onMetric = func(sequence, restored, late uint32) {
			endpoint.Frame(sequence, 0, restored)
			endpoint.Frame(sequence, 1, late)
		}
	}
	ticker := time.NewTicker(frameDuration)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changes:
			buffers.Prune(state)
		case <-ticker.C:
			// A queued ticker timestamp predates a scheduler/decoder stall.
			// Use the actual consumer time for both recovery and deadlines.
			now := time.Now()
			buffers.Prune(state)
			buffers.RemoveInactive(now, DefaultStreamIdleTimeout)
			if err := sendOrderedFrames(ctx, orderedCh, buffers.TickAt(now)); err != nil {
				return err
			}
		case frame, ok := <-mediaCh:
			if !ok {
				buffers.Prune(state)
				return sendOrderedFrames(ctx, orderedCh, buffers.Flush())
			}
			buffers.Prune(state)
			var ready []audio.MediaFrame
			if state != nil {
				state.mu.RLock()
				valid := true
				frames := frame.Batch
				if len(frames) == 0 {
					frames = []audio.MediaFrame{frame}
				}
				for _, f := range frames {
					if !state.voiceMediaAllowedLocked(f.SenderID, f.Voice) {
						valid = false
						break
					}
				}
				if valid {
					ready = buffers.Push(frame)
				}
				state.mu.RUnlock()
			} else {
				ready = buffers.Push(frame)
			}

			if err := sendOrderedFrames(ctx, orderedCh, ready); err != nil {
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
