package media

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"log"
	"net"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
	"uniclog.io/sonoryx/internal/domain"
)

func (m *Manager) forward(p *publisher, remote *webrtc.TrackRemote) {
	defer m.StopPublisher(p.ownerID, p.id)
	var frame *videoFrame
	var timestamp uint32
	valid := false
	var subscribers []*subscriber
	reset := func() {
		frame = nil
		valid = false
		m.invalidateFrame(p)
	}
	deliver := func(item receivedPacket) {
		packet := item.packet
		if len(packet.Payload) == 0 {
			return
		}
		start, key, packetValid := frameMetadata(p.codec.MimeType, packet.Payload)
		if frame == nil || packet.Timestamp != timestamp {
			if frame != nil {
				p.partialFrames.Add(1)
				reset()
			}
			frame = &videoFrame{at: item.at, key: key}
			timestamp = packet.Timestamp
			valid = start && packetValid
		}
		valid = valid && packetValid
		frame.key = frame.key || key
		if item.at.Before(frame.at) {
			frame.at = item.at
		}
		frame.bytes += packet.MarshalSize()
		if frame.bytes > maxFrameBytes || len(frame.packets) >= maxFramePackets {
			valid = false
		}
		if valid {
			frame.packets = append(frame.packets, packet)
		}
		if !packet.Marker {
			return
		}
		if time.Since(frame.at) > frame.maxAge() {
			p.expiredFrames.Add(1)
			reset()
			return
		}
		if valid {
			frame.readyAt = time.Now()
			assembly := frame.readyAt.Sub(frame.at).Milliseconds()
			storeMax(&p.assemblyAge, assembly)
			if frame.key {
				p.keyFrames.Add(1)
				storeMax(&p.keyFrameMaxBytes, int64(frame.bytes))
			}
			subscribers = subscribers[:0]
			p.mu.RLock()
			for _, s := range p.subscribers {
				subscribers = append(subscribers, s)
			}
			p.mu.RUnlock()
			for _, s := range subscribers {
				s.enqueue(p, frame)
			}
			// Do not retain detached subscribers in the reused snapshot backing array.
			clear(subscribers)
		} else {
			p.invalidFrames.Add(1)
			reset()
		}
		frame = nil
	}
	var reorder rtpReorder
	inputLoss := func() {
		p.inputGaps.Add(1)
		reset()
	}
	var deadline time.Time
	for {
		nextDeadline := reorder.deadline()
		if frame != nil && (nextDeadline.IsZero() || frame.at.Add(frame.maxAge()).Before(nextDeadline)) {
			nextDeadline = frame.at.Add(frame.maxAge())
		}
		if !nextDeadline.Equal(deadline) {
			if err := remote.SetReadDeadline(nextDeadline); err != nil {
				return
			}
			deadline = nextDeadline
		}
		packet, _, err := remote.ReadRTP()
		now := time.Now()
		if err != nil {
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() && !isClosed(p.closed) {
				reorder.expire(now, deliver, inputLoss)
				p.inLost.Store(reorder.lost)
				if frame != nil && now.Sub(frame.at) >= frame.maxAge() {
					p.expiredFrames.Add(1)
					reset()
				}
				continue
			}
			log.Printf("screen publisher RTP ended: stream=%d error=%v", p.id, err)
			return
		}
		p.inBytes.Add(uint64(packet.MarshalSize()))
		p.inPackets.Add(1)
		reorder.expire(now, deliver, inputLoss)
		reorder.push(packet, now, deliver, inputLoss)
		p.inFilled.Store(reorder.filled)
		p.inLost.Store(reorder.lost)
	}
}

// Raise a since-last-log maximum; the metrics worker resets it with Swap(0).
func storeMax(value *atomic.Int64, candidate int64) {
	for previous := value.Load(); candidate > previous && !value.CompareAndSwap(previous, candidate); previous = value.Load() {
	}
}

func (p *publisher) close() {
	first := false
	p.closeOnce.Do(func() { close(p.closed); first = true })
	if !first {
		return
	}
	p.mu.Lock()
	subscribers := p.subscribers
	p.subscribers = make(map[string]*subscriber)
	p.ready = false
	p.mu.Unlock()
	for _, s := range subscribers {
		s.close()
	}
	_ = p.pc.Close()
}

func newStreamID(existing map[domain.StreamID]*publisher) (domain.StreamID, error) {
	for i := 0; i < 8; i++ {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		id := domain.StreamID(binary.BigEndian.Uint64(b[:]))
		if id != 0 {
			if _, ok := existing[id]; !ok {
				return id, nil
			}
		}
	}
	return 0, errors.New("cannot allocate screen stream ID")
}

func (m *Manager) invalidateFrame(p *publisher) {
	p.mu.RLock()
	subscribers := make([]*subscriber, 0, len(p.subscribers))
	for _, s := range p.subscribers {
		subscribers = append(subscribers, s)
	}
	p.mu.RUnlock()
	for _, s := range subscribers {
		if !s.connected.Load() {
			continue
		}
		s.queueMu.Lock()
		s.discardQueue()
		s.waitingKeyframe = true
		s.queueMu.Unlock()
		requestRecoveryKeyframe(p, s)
	}
}

// forwardAudio relays the stream sound as is. Opus packets are independent,
// so there is no frame assembly, keyframe recovery or per-viewer queue: a
// write to a viewer is a non-blocking UDP send, and a lost packet stays lost.
// Its end never stops the video publisher.
func (m *Manager) forwardAudio(p *publisher, remote *webrtc.TrackRemote) {
	var subscribers []*subscriber
	for {
		packet, _, err := remote.ReadRTP()
		if err != nil {
			if !isClosed(p.closed) {
				log.Printf("screen publisher audio ended: stream=%d error=%v", p.id, err)
			}
			return
		}
		if len(packet.Payload) == 0 {
			continue
		}
		p.audioInPackets.Add(1)
		// Header extension ids were negotiated with the publisher, not with
		// the viewers; each viewer's interceptors add their own.
		packet.Header.Extension = false
		packet.Header.Extensions = nil
		subscribers = subscribers[:0]
		p.mu.RLock()
		for _, s := range p.subscribers {
			if s.audioTrack != nil {
				subscribers = append(subscribers, s)
			}
		}
		p.mu.RUnlock()
		for _, s := range subscribers {
			if !s.connected.Load() || !m.hub.CanSubscribeScreen(s.sessionID, p.id) {
				continue
			}
			// WriteRTP copies the header before setting SSRC and payload type.
			if s.audioTrack.WriteRTP(packet) == nil {
				s.audioOutPackets.Add(1)
			}
		}
		clear(subscribers)
	}
}
