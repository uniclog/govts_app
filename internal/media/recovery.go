package media

import (
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

func requestKeyframe(p *publisher) {
	p.mu.RLock()
	ssrc := p.ssrc
	p.mu.RUnlock()
	if ssrc != 0 {
		_ = p.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(ssrc)}})
	}
}

func requestRecoveryKeyframe(p *publisher, s *subscriber) {
	now := time.Now().UnixNano()
	previous := s.lastRecoveryPLI.Load()
	if previous != 0 && time.Duration(now-previous) < recoveryPLIInterval {
		return
	}
	if !s.lastRecoveryPLI.CompareAndSwap(previous, now) {
		return
	}
	requestKeyframe(p)
}

func requestStartupKeyframes(p *publisher, s *subscriber) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for attempts := 1; attempts < startupPLIRequests; attempts++ {
		select {
		case <-p.closed:
			return
		case <-s.closed:
			return
		case <-ticker.C:
			requestRecoveryKeyframe(p, s)
		}
	}
}

func drainAudioRTCP(sender *webrtc.RTPSender) {
	for {
		if _, _, err := sender.ReadRTCP(); err != nil {
			return
		}
	}
}

func drainRTCP(p *publisher, s *subscriber, sender *webrtc.RTPSender) {
	for {
		packets, _, err := sender.ReadRTCP()
		if err != nil {
			return
		}
		for _, packet := range packets {
			switch packet.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				s.plis.Add(1)
				requestRecoveryKeyframe(p, s)
			case *rtcp.TransportLayerNack:
				s.nacks.Add(1)
				requestRecoveryKeyframe(p, s)
			}
		}
	}
}
