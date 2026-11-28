package media

import (
	"sync/atomic"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
)

// One bounded worker per publisher coalesces all recovery sources. Closing the
// PeerConnection unblocks RTCP I/O; closed cancels the pending timer.
func requestRecoveryKeyframe(p *publisher, s *subscriber) {
	select {
	case <-p.closed:
		return
	case <-s.closed:
		return
	default:
	}
	select {
	case p.recovery <- struct{}{}:
	default:
		p.pliMerged.Add(1)
	}
}

func runRecovery(p *publisher) {
	runRecoveryLoop(p.closed, p.recovery, recoveryPLIInterval, &p.pliMerged, func() {
		p.mu.RLock()
		ssrc := p.ssrc
		p.mu.RUnlock()
		if ssrc != 0 {
			if err := p.pc.WriteRTCP([]rtcp.Packet{&rtcp.PictureLossIndication{MediaSSRC: uint32(ssrc)}}); err == nil {
				p.pliSent.Add(1)
			}
		}
	})
}

func runRecoveryLoop(closed <-chan struct{}, requests <-chan struct{}, interval time.Duration, merged *atomic.Uint64, send func()) {
	var last time.Time
	for {
		select {
		case <-closed:
			return
		case <-requests:
		}
		if delay := time.Until(last.Add(interval)); delay > 0 {
			merged.Add(1)
			timer := time.NewTimer(delay)
			select {
			case <-closed:
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		select {
		case <-requests:
			merged.Add(1)
		default:
		}
		if isClosed(closed) {
			return
		}
		send()
		last = time.Now()
	}
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
			if s.hasFrame.Load() {
				return
			}
			requestRecoveryKeyframe(p, s)
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
			handleFeedback(p, s, packet)
		}
	}
}

func handleFeedback(p *publisher, s *subscriber, packet rtcp.Packet) {
	switch packet := packet.(type) {
	case *rtcp.ReceiverReport:
		for _, report := range packet.Reports {
			if report.SSRC == s.ssrc {
				s.rr.Store(&receiverStats{highest: report.LastSequenceNumber, lost: report.TotalLost})
			}
		}
	case *rtcp.ReceiverEstimatedMaximumBitrate:
		s.remb.Store(int64(packet.Bitrate))
		s.rembAt.Store(time.Now().UnixNano())
	case *rtcp.FullIntraRequest:
		s.firs.Add(1)
		requestRecoveryKeyframe(p, s)
	case *rtcp.PictureLossIndication:
		s.plis.Add(1)
		requestRecoveryKeyframe(p, s)
	case *rtcp.TransportLayerNack:
		s.nacks.Add(1) // NACK is handled by Pion, not converted into PLI.
	}
}

// Interceptors process RTCP only while it is read; viewer audio feedback
// carries nothing the SFU acts on.
func drainAudioRTCP(sender *webrtc.RTPSender) {
	for {
		if _, _, err := sender.ReadRTCP(); err != nil {
			return
		}
	}
}
