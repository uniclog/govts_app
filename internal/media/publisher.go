package media

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"log"
	"strings"

	"github.com/pion/webrtc/v4"
	"uniclog.io/govts/internal/domain"
)

func (m *Manager) forward(p *publisher, remote *webrtc.TrackRemote) {
	defer m.StopPublisher(p.ownerID, p.id)
	m.pump(p, remote, false)
}

func (m *Manager) forwardAudio(p *publisher, remote *webrtc.TrackRemote) {
	m.pump(p, remote, true)
}

func (m *Manager) pump(p *publisher, remote *webrtc.TrackRemote, audio bool) {
	for {
		packet, _, err := remote.ReadRTP()
		if err != nil {
			if audio {
				log.Printf("screen publisher audio RTP ended: stream_id=%d owner_session_id=%d error=%v", p.id, p.ownerID, err)
			} else {
				log.Printf("screen publisher RTP ended: stream_id=%d owner_session_id=%d error=%v", p.id, p.ownerID, err)
			}
			return
		}
		p.inBytes.Add(uint64(packet.MarshalSize()))
		p.inPackets.Add(1)
		var needsRecovery []*subscriber
		p.mu.RLock()
		for _, s := range p.subscribers {
			if !m.hub.CanSubscribeScreen(s.sessionID, p.id) {
				continue
			}
			queue := s.packets
			if audio {
				queue = s.audioPackets
			}
			if queue == nil {
				continue
			}
			clone := packet.Clone()
			select {
			case queue <- clone:
			default:
				s.drops.Add(1)
				if !audio {
					needsRecovery = append(needsRecovery, s)
				}
			}
		}
		p.mu.RUnlock()
		for _, s := range needsRecovery {
			requestRecoveryKeyframe(p, s)
		}
	}
}

func offerHasAudio(sdp string) bool {
	for _, line := range strings.Split(sdp, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "m=audio") {
			return true
		}
	}
	return false
}

func (p *publisher) close() {
	p.closeOnce.Do(func() {
		close(p.closed)
		p.mu.Lock()
		subscribers := p.subscribers
		p.subscribers = make(map[string]*subscriber)
		p.mu.Unlock()
		for _, s := range subscribers {
			s.close()
		}
		_ = p.pc.Close()
	})
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
