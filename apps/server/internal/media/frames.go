package media

import (
	"strings"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/rtp/codecs"
	"github.com/pion/rtp/codecs/av1/obu"
)

// The publisher owns freshly allocated ReadRTP buffers. A completed frame is
// immutable and shared by bounded queues. Writers clone only headers (including
// CSRC/extensions); Pion's default NACK packet factory copies cached payloads.
// No buffer pool or reference counter is needed: Go retains data until the last
// queue/writer releases it. Assembly <=4 MiB/2048 packets, each queue <=8 frames/2048 packets
// and <=4 MiB; each subscriber additionally has a 2048-packet Pion NACK cache.
type videoFrame struct {
	packets []*rtp.Packet
	at      time.Time
	readyAt time.Time
	bytes   int
	key     bool
}

// at is the first received packet and readyAt the completed assembly. Queue
// age starts at readyAt, but readyAt never resets the frame's total age: a
// frame is stale once either budget is exceeded. Frames constructed without
// readyAt retain their original age semantics.
func (f *videoFrame) queueTime() time.Time {
	if !f.readyAt.IsZero() {
		return f.readyAt
	}
	return f.at
}

// A keyframe can be many times larger than a delta frame; the publisher's own
// pacer and the viewer's pacer both spread it over time. Dropping it on the
// delta budget would only trigger another keyframe of the same size, so a
// keyframe has keyFrameMaxAge for assembly, queueing and writing.
func (f *videoFrame) maxAge() time.Duration {
	if f.key {
		return keyFrameMaxAge
	}
	return frameMaxAge
}

// expiresAt is the moment the frame becomes stale: the earlier of its total
// and queue budgets. backlog gives a delta frame queued behind a keyframe the
// keyframe budget (subscriber.backlogUntil): it waited for that keyframe's write,
// and dropping it would only request another keyframe of the same size.
func (f *videoFrame) expiresAt(backlog bool) time.Time {
	maxAge, queueAge := f.maxAge(), subscriberQueueAge
	if f.key || backlog {
		maxAge, queueAge = keyFrameMaxAge, keyFrameMaxAge
	}
	total, queued := f.at.Add(maxAge), f.queueTime().Add(queueAge)
	if queued.Before(total) {
		return queued
	}
	return total
}

func (f *videoFrame) stale(now time.Time, backlog bool) bool {
	return now.After(f.expiresAt(backlog))
}

func frameMetadata(codec string, payload []byte) (start, key, valid bool) {
	if len(payload) == 0 {
		return
	}
	switch strings.ToLower(codec) {
	case "video/vp8":
		var p codecs.VP8Packet
		if _, err := p.Unmarshal(payload); err == nil && len(p.Payload) > 0 {
			valid = true
			start = p.S == 1 && p.PID == 0
			key = start && len(p.Payload) > 0 && p.Payload[0]&1 == 0
		}
	case "video/vp9":
		var p codecs.VP9Packet
		if _, err := p.Unmarshal(payload); err == nil && len(p.Payload) > 0 {
			valid = true
			start = p.B && (!p.L || p.SID == 0)
			key = start && !p.P
		}
	case "video/av1":
		if !validAV1Aggregation(payload) {
			return
		}
		start = payload[0]&0x80 == 0        // Z: first OBU continues the previous packet.
		key = start && payload[0]&0x08 != 0 // N: new coded video sequence.
		valid = true
	case "video/h264":
		if len(payload) < 2 || payload[0]&0x80 != 0 {
			return
		}
		typ := payload[0] & 31
		if typ == 0 || (typ > 24 && typ != 28) {
			return
		}
		start = typ >= 1 && typ <= 24
		key = typ == 5
		if typ == 28 {
			if len(payload) < 3 || payload[1]&0xc0 == 0xc0 || payload[1]&0x20 != 0 || payload[1]&31 == 0 || payload[1]&31 >= 24 {
				return false, false, false
			}
			start = payload[1]&0x80 != 0
			key = start && payload[1]&31 == 5
		}
		if typ == 24 { // STAP-A: detect an IDR in the aggregation.
			i := 1
			for i+2 <= len(payload) {
				n := int(payload[i])<<8 | int(payload[i+1])
				i += 2
				if n < 2 || i+n > len(payload) || payload[i]&0x80 != 0 || payload[i]&31 == 0 || payload[i]&31 >= 24 {
					return false, false, false
				}
				key = key || payload[i]&31 == 5
				i += n
			}
			if i != len(payload) {
				return false, false, false
			}
		}
		valid = true
	}
	return
}

// Validate the AV1 RTP aggregation lengths without allocating OBU slices.
func validAV1Aggregation(payload []byte) bool {
	if len(payload) < 2 || payload[0]&7 != 0 || payload[0]&0x88 == 0x88 {
		return false
	}
	w := int(payload[0]>>4) & 3
	for offset, element := 1, 1; offset < len(payload); element++ {
		if w != 0 && element == w {
			return true // Last OBU consumes the remaining bytes without a length field.
		}
		length, read, err := obu.ReadLeb128(payload[offset:])
		if err != nil || length == 0 || uint64(length) > uint64(len(payload)-offset-int(read)) {
			return false
		}
		offset += int(read) + int(length)
		if offset == len(payload) {
			return w == 0
		}
	}
	return false
}

func (s *subscriber) discardQueue() {
	for {
		select {
		case frame := <-s.packets:
			s.drops.Add(uint64(len(frame.packets)))
			s.taken()
			if frame.key {
				s.keyWritten()
			}
		default:
			s.queuedBytes = 0
			s.queuedPackets = 0
			return
		}
	}
}

func (s *subscriber) enqueue(p *publisher, frame *videoFrame) {
	if !s.connected.Load() {
		return
	}
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	select {
	case <-s.closed:
		return
	default:
	}
	// Invalid input cannot become a recovery frame merely because it is a keyframe.
	if frame.bytes > maxFrameBytes || len(frame.packets) > maxFramePackets || frame.stale(time.Now(), s.keysPending > 0) {
		s.discardQueue()
		s.waitingKeyframe = true
		s.drops.Add(uint64(len(frame.packets)))
		requestQueueRecovery(p, s)
		return
	}
	// Bound both age and bytes, not just the number of frame entries.
	bytes := frame.bytes + s.queuedBytes
	if bytes > maxFrameBytes || s.queuedPackets+len(frame.packets) > maxFramePackets || len(s.packets) >= s.queueLimit() {
		s.congestion.Add(1)
		s.discardQueue()
		s.waitingKeyframe = true
		requestQueueRecovery(p, s)
	}
	if s.waitingKeyframe {
		if !frame.key {
			s.drops.Add(uint64(len(frame.packets)))
			requestQueueRecovery(p, s)
			return
		}
		s.waitingKeyframe = false
	}
	select {
	case s.packets <- frame:
		s.queuedBytes += frame.bytes
		s.queuedPackets += len(frame.packets)
		s.queuedTotal++
		if frame.key {
			s.keysPending++
		}
		if s.keysPending > 0 {
			s.backlogUntil = s.queuedTotal
		}
		select {
		case s.wake <- struct{}{}:
		default:
		}
	default:
		s.congestion.Add(1)
		s.discardQueue()
		s.waitingKeyframe = true
		s.drops.Add(uint64(len(frame.packets)))
		requestQueueRecovery(p, s)
	}
}

func isClosed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

// A single slow viewer should not continually disturb the shared encoder; a
// viewer excluded from the adaptive source ceiling waits longer.
func requestQueueRecovery(p *publisher, s *subscriber) {
	now := time.Now().UnixNano()
	previous := s.lastQueueRecovery.Load()
	interval := queueRecoveryInterval
	if s.slow.Load() {
		interval = slowQueueRecovery
	}
	if previous != 0 && time.Duration(now-previous) < interval {
		return
	}
	if s.lastQueueRecovery.CompareAndSwap(previous, now) {
		requestRecoveryKeyframe(p, s)
	}
}

// Frame entries the queue may hold; caller holds queueMu.
func (s *subscriber) queueLimit() int {
	if s.keysPending > 0 || s.backlogUntil > s.takenTotal {
		return backlogQueueSize
	}
	return subscriberQueueSize
}

// Records a frame leaving the queue and reports whether it was queued behind a
// keyframe write; caller holds queueMu.
func (s *subscriber) taken() (backlog bool) {
	s.takenTotal++
	return s.takenTotal <= s.backlogUntil
}

// A keyframe left the backlog: its write ended or it was dropped; caller holds queueMu.
func (s *subscriber) keyWritten() {
	s.keysPending = max(0, s.keysPending-1)
}
