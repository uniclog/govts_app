package media

import (
	"log"
	"time"
)

func (s *subscriber) close() {
	first := false
	s.closeOnce.Do(func() { close(s.closed); first = true })
	if !first {
		return
	}
	if s.closingGauge != nil {
		s.closingGauge.Add(1)
		defer s.closingGauge.Add(-1)
	}
	s.connected.Store(false)
	// Producer and consumer share queueMu; only the signal channel is closed.
	// Queued payload references are released before closing Pion outside locks.
	s.queueMu.Lock()
	s.discardQueue()
	s.queueMu.Unlock()
	_ = s.pc.Close() // unblocks sender.ReadRTCP and the RTP transport writer
}

func (m *Manager) runSubscriber(p *publisher, s *subscriber) {
	defer m.removeSubscriber(p, s)
	var pace pacer
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	defer timer.Stop()
	for {
		select {
		case <-s.closed:
			return
		case <-s.wake:
		}
		for {
			s.queueMu.Lock()
			var frame *videoFrame
			var backlog bool
			select {
			case frame = <-s.packets:
				s.queuedBytes -= frame.bytes
				s.queuedPackets -= len(frame.packets)
				backlog = s.taken()
			default:
			}
			if frame == nil {
				s.queueMu.Unlock()
				break
			}
			now := time.Now()
			s.queueAge.Store(now.Sub(frame.queueTime()).Milliseconds())
			s.totalAge.Store(now.Sub(frame.at).Milliseconds())
			if frame.stale(now, backlog) {
				if frame.key {
					s.keyWritten()
				}
				s.congestion.Add(1)
				s.drops.Add(uint64(len(frame.packets)))
				s.discardQueue()
				s.waitingKeyframe = true
				s.queueMu.Unlock()
				requestQueueRecovery(p, s)
				continue
			}
			s.queueMu.Unlock()
			s.writingAt.Store(frame.queueTime().UnixNano())
			s.writingFrameAt.Store(frame.at.UnixNano())
			sent := true
			expires := frame.expiresAt(backlog)
			remaining := frame.bytes
			for index, packet := range frame.packets {
				size := packet.MarshalSize()
				if m.adaptive {
					now := time.Now()
					if wait := pace.reserve(now, size, framePaceRate(s.paceRate(), remaining, now, expires)); wait >= time.Millisecond {
						timer.Reset(wait)
						select {
						case <-s.closed:
							timer.Stop()
							return
						case <-timer.C:
						}
					}
				}
				remaining -= size
				if frame.stale(time.Now(), backlog) {
					s.congestion.Add(1)
					s.queueMu.Lock()
					s.drops.Add(uint64(len(frame.packets) - index))
					s.discardQueue()
					s.waitingKeyframe = true
					s.queueMu.Unlock()
					requestQueueRecovery(p, s)
					sent = false
					break
				}
				select {
				case <-s.closed:
					return
				default:
				}
				if !m.hub.CanSubscribeScreen(s.sessionID, p.id) {
					return
				}
				local := *packet
				local.Header = packet.Header.Clone()
				// Frames intentionally dropped by the SFU must not create downstream
				// NACK gaps for packets that were never sent to this subscriber.
				if !s.sequenceStarted {
					s.nextSequence = packet.SequenceNumber
					s.sequenceStarted = true
				}
				local.Header.SequenceNumber = s.nextSequence
				s.nextSequence++
				if err := s.track.WriteRTP(&local); err != nil {
					log.Printf("screen subscriber RTP failed: stream=%d session=%d error=%v", p.id, s.sessionID, err)
					return
				}
				s.outBytes.Add(uint64(size))
				s.outPackets.Add(1)
			}
			s.writingAt.Store(0)
			s.writingFrameAt.Store(0)
			if frame.key {
				// Frames queued from now on no longer wait for this keyframe.
				s.queueMu.Lock()
				s.keyWritten()
				s.queueMu.Unlock()
			}
			if sent {
				s.hasFrame.Store(true)
			}
		}
	}
}
