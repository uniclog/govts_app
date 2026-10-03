package media

import "log"

func (s *subscriber) close() {
	s.closeOnce.Do(func() {
		close(s.closed)
		_ = s.pc.Close()
	})
}

func (m *Manager) runSubscriber(p *publisher, s *subscriber) {
	for {
		select {
		case <-s.closed:
			return
		case packet := <-s.packets:
			if !m.hub.CanSubscribeScreen(s.sessionID, p.id) {
				m.Unsubscribe(s.sessionID, p.id, s.id)
				return
			}
			if err := s.track.WriteRTP(packet); err != nil {
				log.Printf("screen subscriber RTP failed: stream_id=%d session_id=%d subscriber=%q error=%v", p.id, s.sessionID, s.id, err)
				s.close()
				return
			}
			s.outBytes.Add(uint64(packet.MarshalSize()))
			s.outPackets.Add(1)
		case packet := <-s.audioPackets:
			if s.audio == nil || packet == nil {
				continue
			}
			if err := s.audio.WriteRTP(packet); err != nil {
				log.Printf("screen subscriber audio RTP failed: stream_id=%d session_id=%d subscriber=%q error=%v", p.id, s.sessionID, s.id, err)
				s.close()
				return
			}
			s.outBytes.Add(uint64(packet.MarshalSize()))
			s.outPackets.Add(1)
		}
	}
}
