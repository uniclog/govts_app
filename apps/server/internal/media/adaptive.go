package media

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/pion/rtcp"
)

// Adaptive mode (Config.AdaptiveBitrate, off by default). Once per second the
// reconcile worker updates a loss based rate estimate for every viewer
// and sends the publisher a REMB with the lowest estimate among viewers that
// are not far behind the rest. libwebrtc senders apply a received REMB as an
// upper bound on their target rate, also when transport-cc is negotiated, so
// the client needs no changes and its maxBitrate stays the ceiling. A viewer
// has no estimate (no cap, no pacing) until it reports loss over enough packets
// or a fresh REMB; then the estimate drops at once and grows by at most
// rateGrowth per second. Viewer
// writers pace primary RTP to the estimate minus measured RTX. When disabled
// nothing here runs and no REMB is sent. Transport-wide sequence numbers are
// not used: each PeerConnection keeps its own Pion interceptor state.
const (
	minViewerRate     = 300_000     // bps; estimates never go below it
	releasedRate      = 100_000_000 // bps; REMB that lifts the cap, maxBitrate still applies
	rateBackoff       = 0.85
	rateGrowth        = 1.08 // per second, only while the link is healthy
	rateAppHeadroom   = 1.5  // growth stops at this multiple of the delivered rate
	congestedLoss     = 0.10
	healthyLoss       = 0.02
	slowViewerShare   = 0.25 // of the median estimate; slower viewers do not cap the source
	sourceHeadroom    = 0.9  // leaves room for RTX and RTCP on the slowest counted link
	paceFactor        = 2.5  // as libwebrtc; lets a keyframe drain within the age budget
	paceBurst         = 20 * time.Millisecond
	slowQueueRecovery = 10 * time.Second
	minLossPackets    = 100 // packets a loss measurement must cover
	maxLossWindow     = 5 * time.Second
	rembMaxAge        = 5 * time.Second // a viewer REMB older than this is ignored
	// An estimate with no limit signal (loss of at least healthyLoss or a
	// fresh REMB) for this long is dropped: the viewer is uncapped and unpaced
	// again, so a short loss burst on an idle screen does not cap it forever.
	rateReleaseAfter = 30 * time.Second
	// Share of the time left before a frame expires that pacing may use.
	paceDeadlineShare = 0.8
)

// Owned by the reconcile worker.
type rateSample struct {
	at          time.Time
	bytes       uint64
	repeatBytes uint64
}

func (m *Manager) adaptRates(p *publisher, subscribers []*subscriber, now time.Time) {
	p.mu.RLock()
	ssrc := p.ssrc
	p.mu.RUnlock()
	rates := make([]int64, 0, len(subscribers))
	active := 0
	for _, s := range subscribers {
		rate, ok := m.updateRate(s, now)
		if !ok {
			continue
		}
		active++
		if rate > 0 {
			rates = append(rates, rate)
		}
	}
	ceiling := int64(releasedRate)
	var threshold int64
	if len(rates) > 0 {
		slices.Sort(rates)
		// Active viewers without an estimate have shown no limit; they rank as
		// releasedRate above every estimate, so a minority of lossy viewers
		// cannot become the median and cap the healthy majority.
		median := int64(releasedRate)
		if middle := active / 2; middle < len(rates) {
			median = rates[middle]
		}
		threshold = max(minViewerRate, int64(float64(median)*slowViewerShare))
		// When every estimate is below the threshold, nothing caps the source.
		if index, _ := slices.BinarySearch(rates, threshold); index < len(rates) {
			ceiling = max(minViewerRate, int64(float64(rates[index])*sourceHeadroom))
		}
	}
	for _, s := range subscribers {
		rate := s.rate.Load()
		s.slow.Store(rate > 0 && rate < threshold)
	}
	p.ceiling.Store(ceiling)
	if ssrc != 0 {
		_ = p.pc.WriteRTCP([]rtcp.Packet{&rtcp.ReceiverEstimatedMaximumBitrate{Bitrate: float32(ceiling), SSRCs: []uint32{uint32(ssrc)}}})
	}
}

// Returns the viewer's estimate in bps (0 while it has none) and whether the
// viewer is active: connected and already receiving frames.
func (m *Manager) updateRate(s *subscriber, now time.Time) (int64, bool) {
	if !s.connected.Load() || !s.hasFrame.Load() {
		return 0, false
	}
	current := rateSample{at: now, bytes: s.outBytes.Load(), repeatBytes: m.repeatBytes(s)}
	previous := s.sample
	s.sample = current
	elapsed := now.Sub(previous.at).Seconds()
	if previous.at.IsZero() || elapsed <= 0 {
		return s.rate.Load(), true
	}
	repeated := float64(current.repeatBytes-previous.repeatBytes) * 8 / elapsed
	delivered := float64(current.bytes-previous.bytes)*8/elapsed + repeated
	s.rtxRate.Store(int64(repeated))
	loss, hasLoss := s.intervalLoss(now)
	rate := float64(s.rate.Load())
	remb := s.remb.Load()
	if now.Sub(time.Unix(0, s.rembAt.Load())) > rembMaxAge {
		remb = 0
	}
	// Only network signals (receiver report loss, the viewer's REMB) set or
	// lower the estimate. Server queue overflows usually mean local CPU or
	// socket pressure, and once the writer is paced it can make frames stale
	// by itself; counting either would feed back into ever slower pacing.
	// They stay visible as congestion_events.
	congested := hasLoss && loss > congestedLoss
	if congested || remb > 0 || hasLoss && loss >= healthyLoss {
		s.limitAt = now
	} else if rate > 0 && now.Sub(s.limitAt) > rateReleaseAfter {
		s.rate.Store(0)
		return 0, true
	}
	switch {
	case rate == 0 && !congested && remb == 0:
		// No evidence of a limit yet: neither cap the source nor pace.
		return 0, true
	case rate == 0 && congested:
		rate = delivered * rateBackoff
	case rate == 0:
		rate = float64(remb)
	case congested:
		rate = min(rate, delivered) * rateBackoff
	case !hasLoss || loss < healthyLoss:
		// An idle screen sends little; do not grow far past what was tested.
		rate = min(rate*rateGrowth, max(rate, delivered*rateAppHeadroom))
	}
	if remb > 0 {
		rate = min(rate, float64(remb))
	}
	estimate := max(int64(rate), minViewerRate)
	s.rate.Store(estimate)
	return estimate, true
}

func (m *Manager) repeatBytes(s *subscriber) uint64 {
	if value, ok := m.meters.streams.Load(transportKey{s.pc.ID(), s.ssrc}); ok {
		return value.(*transportMeter).repeatBytes.Load()
	}
	return 0
}

// Cumulative counters from the viewer's latest RTCP receiver report for the
// primary SSRC: extended highest sequence number and packets lost.
type receiverStats struct{ highest, lost uint32 }

// Owned by the reconcile worker: the report counters at the window start.
type lossBase struct {
	at      time.Time
	highest uint32
	lost    uint32
}

// Loss over the packets the viewer reported since the window started, rather
// than the RR fraction of one report: a signal needs minLossPackets, so a
// quiet stream with a few lost packets gives none, and a window that cannot
// collect them within maxLossWindow restarts. Each report is counted once
// because the window advances past it.
func (s *subscriber) intervalLoss(now time.Time) (float64, bool) {
	report := s.rr.Load()
	if report == nil {
		return 0, false
	}
	base := &s.lossBase
	expected := report.highest - base.highest
	if base.at.IsZero() || int32(expected) < 0 {
		*base = lossBase{now, report.highest, report.lost}
		return 0, false
	}
	if expected < minLossPackets {
		if now.Sub(base.at) > maxLossWindow {
			*base = lossBase{now, report.highest, report.lost}
		}
		return 0, false
	}
	// RR cumulative lost is a signed 24-bit counter and may decrease after
	// duplicates; take the sign-extended difference and clamp it.
	lost := int32((report.lost-base.lost)<<8) >> 8
	*base = lossBase{now, report.highest, report.lost}
	loss := min(1, max(0, float64(lost)/float64(expected)))
	s.lastLoss.Store(int32(loss*1000) + 1)
	return loss, true
}

// Pacing rate for the writer: the estimate times paceFactor, minus the RTX the
// NACK responder sends outside the writer. 0 disables pacing.
func (s *subscriber) paceRate() int64 {
	rate := s.rate.Load()
	if rate == 0 {
		return 0
	}
	return max(minViewerRate, int64(float64(rate)*paceFactor)-s.rtxRate.Load())
}

// Pacing must not make a frame stale by itself: a keyframe larger than the
// viewer's pace allows within its budget would be dropped, a new keyframe
// requested and the viewer kept frozen. When the remaining bytes do not fit
// into paceDeadlineShare of the time left at rate, the rest of the frame is
// sent at the rate that fits, or at once when no time is left. 0 disables pacing.
func framePaceRate(rate int64, remaining int, now, expires time.Time) int64 {
	if rate <= 0 {
		return 0
	}
	left := time.Duration(float64(expires.Sub(now)) * paceDeadlineShare)
	if left <= 0 {
		return 0
	}
	return max(rate, int64(float64(remaining*8)/left.Seconds()))
}

// Owned by the subscriber writer. The first paceBurst of data is sent at once;
// after that each packet waits for its slot. The writer holds no lock while it
// waits and still checks frame age before every packet, so pacing cannot grow
// a second queue: frames that become stale are dropped by the existing limits.
type pacer struct{ next time.Time }

func (p *pacer) reserve(now time.Time, bytes int, rate int64) time.Duration {
	if rate <= 0 {
		return 0
	}
	if earliest := now.Add(-paceBurst); p.next.Before(earliest) {
		p.next = earliest
	}
	wait := p.next.Sub(now)
	p.next = p.next.Add(time.Duration(float64(bytes*8) / float64(rate) * float64(time.Second)))
	return wait
}

// Suffix for the screen metrics line; empty when adaptive mode is off. Rates
// are the latest values in kbps, na until known; congestion_events is cumulative.
func (m *Manager) adaptiveMetrics(p *publisher, s *subscriber) string {
	if !m.adaptive {
		return ""
	}
	kbps := func(bps int64) string {
		if bps <= 0 {
			return "na"
		}
		return strconv.FormatInt(bps/1000, 10)
	}
	loss := "na"
	if permille := s.lastLoss.Load(); permille > 0 {
		loss = strconv.FormatFloat(float64(permille-1)/10, 'f', 1, 64)
	}
	remb := s.remb.Load()
	if time.Since(time.Unix(0, s.rembAt.Load())) > rembMaxAge {
		remb = 0
	}
	return fmt.Sprintf(" source_ceiling_kbps=%s estimate_kbps=%s rtx_kbps=%d rr_loss_percent=%s viewer_remb_kbps=%s slow=%t congestion_events=%d",
		kbps(p.ceiling.Load()), kbps(s.rate.Load()), s.rtxRate.Load()/1000, loss, kbps(remb), s.slow.Load(), s.congestion.Load())
}
