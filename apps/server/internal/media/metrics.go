package media

import (
	"log"
	"time"
)

func (m *Manager) logMetrics() {
	m.mu.RLock()
	publishers := make([]*publisher, 0, len(m.publishers))
	for _, p := range m.publishers {
		publishers = append(publishers, p)
	}
	m.mu.RUnlock()
	m.logProcessMetrics(len(publishers) > 0)
	now := time.Now()
	for _, p := range publishers {
		p.mu.Lock()
		hasBaseline := !p.metricsAt.IsZero()
		elapsed := now.Sub(p.metricsAt).Seconds()
		inBytes := p.inBytes.Load()
		inPackets := p.inPackets.Load()
		inBitrate := float64(0)
		inPacketsPerSecond := float64(0)
		if hasBaseline && elapsed > 0 {
			inBitrate = float64(inBytes-p.lastInBytes) * 8 / elapsed
			inPacketsPerSecond = float64(inPackets-p.lastInPackets) / elapsed
		}
		p.metricsAt, p.lastInBytes, p.lastInPackets = now, inBytes, inPackets
		type entry struct {
			subscriber            *subscriber
			session               uint64
			kbps, pps             float64
			queue                 int
			drops, pli, fir, nack uint64
			age, totalAge         int64
		}
		entries := make([]entry, 0, len(p.subscribers))
		for _, s := range p.subscribers {
			outBytes := s.outBytes.Load()
			outPackets := s.outPackets.Load()
			outBitrate := float64(0)
			outPacketsPerSecond := float64(0)
			if hasBaseline && elapsed > 0 {
				outBitrate = float64(outBytes-s.lastOutBytes) * 8 / elapsed
				outPacketsPerSecond = float64(outPackets-s.lastOutPackets) / elapsed
			}
			s.lastOutBytes, s.lastOutPackets = outBytes, outPackets
			entries = append(entries, entry{s, s.sessionID, outBitrate / 1000, outPacketsPerSecond, len(s.packets), s.drops.Load(), s.plis.Load(), s.firs.Load(), s.nacks.Load(), s.queueAge.Load(), s.totalAge.Load()})
		}
		p.mu.Unlock()
		assemblyAge := p.assemblyAge.Swap(0)
		keyFrameMaxBytes := p.keyFrameMaxBytes.Swap(0)
		for _, e := range entries {
			if at := e.subscriber.writingAt.Load(); at != 0 {
				if age := time.Since(time.Unix(0, at)).Milliseconds(); age > e.age {
					e.age = age
				}
			}
			if at := e.subscriber.writingFrameAt.Load(); at != 0 {
				if age := time.Since(time.Unix(0, at)).Milliseconds(); age > e.totalAge {
					e.totalAge = age
				}
			}
			var repeats, repeatBytes uint64
			for _, encoding := range e.subscriber.sender.GetParameters().Encodings {
				if value, ok := m.meters.streams.Load(transportKey{e.subscriber.pc.ID(), uint32(encoding.SSRC)}); ok {
					meter := value.(*transportMeter)
					repeats += meter.repeats.Load()
					repeatBytes += meter.repeatBytes.Load()
				}
			}
			log.Printf("screen metrics: stream=%d session=%d active=%d closing=%d in_kbps=%.0f in_pps=%.1f primary_out_kbps=%.0f primary_out_pps=%.1f queue_frames=%d queue_age_ms=%d total_age_ms=%d assembly_age_ms=%d drops=%d received_pli=%d received_fir=%d received_nack=%d sent_pli=%d merged_pli=%d retransmitted_rtp_packets=%d retransmitted_rtp_bytes=%d in_filled_packets=%d in_lost_packets=%d keyframes=%d keyframe_max_bytes=%d input_gap_events=%d invalid_frames=%d expired_frames=%d partial_frames=%d audio_in_packets=%d audio_out_packets=%d%s", p.id, e.session, len(entries), m.closing.Load(), inBitrate/1000, inPacketsPerSecond, e.kbps, e.pps, e.queue, e.age, e.totalAge, assemblyAge, e.drops, e.pli, e.fir, e.nack, p.pliSent.Load(), p.pliMerged.Load(), repeats, repeatBytes, p.inFilled.Load(), p.inLost.Load(), p.keyFrames.Load(), keyFrameMaxBytes, p.inputGaps.Load(), p.invalidFrames.Load(), p.expiredFrames.Load(), p.partialFrames.Load(), p.audioInPackets.Load(), e.subscriber.audioOutPackets.Load(), m.adaptiveMetrics(p, e.subscriber))
		}
	}
}
