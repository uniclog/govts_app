package media

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/webrtc/v4"
	"uniclog.io/sonoryx/apps/server/internal/voice"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/mediasignal"
)

var (
	ErrPublisherExists    = errors.New("publisher already exists")
	ErrStreamNotReady     = errors.New("screen stream is not ready")
	ErrSubscriptionDenied = errors.New("screen stream subscription denied")
	// ErrMediaLimit rejects a peer connection over a session or server limit.
	ErrMediaLimit = errors.New("screen sharing limit reached")
)

const (
	subscriberQueueSize = 8
	// Frames a queue may hold while it drains a keyframe backlog: 60 FPS over
	// keyFrameMaxAge. Bytes and packets keep their limits.
	backlogQueueSize   = 64
	subscriberQueueAge = 250 * time.Millisecond
	// A frame is useful only while its total age, from its first received
	// packet to the last subscriber write, stays within frameMaxAge. It is an
	// explicit compromise over the original 250 ms so upstream NACK repair
	// (packetRepairAge) still leaves room for queueing; revisit after baseline.
	frameMaxAge     = 400 * time.Millisecond
	keyFrameMaxAge  = time.Second
	packetRepairAge = 200 * time.Millisecond
	// One NACK per interval, stopped once repair could no longer arrive in time.
	nackInterval        = 50 * time.Millisecond
	nackMaxPerPacket    = uint16(packetRepairAge / nackInterval)
	mediaReadBuffer     = 4 * 1024 * 1024
	maxFrameBytes       = 4 * 1024 * 1024
	maxFramePackets     = 2048
	nackResponderCache  = 2048
	recoveryPLIInterval = time.Second
	// Per viewer, for queue overflow or stale frames.
	queueRecoveryInterval = 5 * time.Second
	startupPLIRequests    = 5
	// Subscriptions one session may hold on one stream (a viewer reconnecting
	// overlaps its old and new connection) and across all streams.
	maxSessionStreamSubscriptions = 2
	maxSessionSubscriptions       = 8
	// Peer connections without a UDP port range; a range caps it at its size.
	defaultMaxPeerConnections = 256
)

type screenSessions interface {
	Get(uint64) (voice.Session, bool)
	StartScreenShare(uint64, domain.StreamID) (domain.ScreenStream, error)
	StopScreenShare(uint64, domain.StreamID) error
	ScreenStream(domain.StreamID) (domain.ScreenStream, bool)
	CanSubscribeScreen(uint64, domain.StreamID) bool
}

type Manager struct {
	mu           sync.RWMutex
	hub          screenSessions
	api          *webrtc.API
	publishers   map[domain.StreamID]*publisher
	ownerStreams map[uint64]domain.StreamID
	done         chan struct{}
	closeOnce    sync.Once
	workers      sync.WaitGroup
	meters       *transportMeters
	network      *bufferedMediaNet
	process      processSampler // owned by the metrics worker
	adaptive     bool
	closing      atomic.Int64
	maxPeers     int
}

type Config struct {
	MinUDPPort   uint16
	MaxUDPPort   uint16
	AdvertisedIP string
	// Enables viewer rate estimates, the REMB source ceiling and pacing.
	AdaptiveBitrate bool
}

type publisher struct {
	id       domain.StreamID
	ownerID  uint64
	pc       *webrtc.PeerConnection
	mu       sync.RWMutex
	codec    webrtc.RTPCodecCapability
	ssrc     webrtc.SSRC
	ready    bool
	starting bool
	// hasAudio is fixed from the offer before the publisher is visible;
	// audioStarted (under mu) admits one audio forwarder.
	hasAudio       bool
	audioStarted   bool
	audioInPackets atomic.Uint64
	subscribers    map[string]*subscriber
	closed         chan struct{}
	closeOnce      sync.Once
	createdAt      time.Time
	recovery       chan struct{}
	pliSent        atomic.Uint64
	pliMerged      atomic.Uint64
	inBytes        atomic.Uint64
	inPackets      atomic.Uint64
	// Cumulative event counts (not packets). inputGaps: sequence gaps still
	// unrepaired after packetRepairAge, or sequence discontinuities.
	// invalidFrames: frames ended by a marker without a valid start, with a
	// malformed payload or over frame limits. expiredFrames: frames not
	// completed within frameMaxAge. partialFrames: frames abandoned because a
	// new RTP timestamp arrived before their marker.
	inputGaps     atomic.Uint64
	invalidFrames atomic.Uint64
	expiredFrames atomic.Uint64
	partialFrames atomic.Uint64
	// Largest first-packet-to-completion time since the last metrics log, ms.
	assemblyAge atomic.Int64
	// Cumulative input packets: filled holes (repair or reordering) and
	// sequence numbers given up after packetRepairAge (rtpReorder).
	inFilled atomic.Uint64
	inLost   atomic.Uint64
	// Completed keyframes forwarded (cumulative) and the largest one since
	// the last metrics log, RTP bytes.
	keyFrames        atomic.Uint64
	keyFrameMaxBytes atomic.Int64
	// REMB ceiling sent to the source in adaptive mode, bps.
	ceiling       atomic.Int64
	metricsAt     time.Time
	lastInBytes   uint64
	lastInPackets uint64
}

type subscriber struct {
	id                string
	sessionID         uint64
	pc                *webrtc.PeerConnection
	track             *webrtc.TrackLocalStaticRTP
	sender            *webrtc.RTPSender
	audioTrack        *webrtc.TrackLocalStaticRTP // nil without stream sound; fixed before workers start
	audioOutPackets   atomic.Uint64
	packets           chan *videoFrame
	queueMu           sync.Mutex
	queuedBytes       int
	queuedPackets     int
	wake              chan struct{}
	waitingKeyframe   bool
	connected         atomic.Bool
	queueAge          atomic.Int64
	totalAge          atomic.Int64
	writingAt         atomic.Int64
	writingFrameAt    atomic.Int64
	lastQueueRecovery atomic.Int64
	firs              atomic.Uint64
	closed            chan struct{}
	closeOnce         sync.Once
	closingGauge      *atomic.Int64
	outBytes          atomic.Uint64
	outPackets        atomic.Uint64
	drops             atomic.Uint64
	plis              atomic.Uint64
	nacks             atomic.Uint64
	recoveryOnce      sync.Once
	hasFrame          atomic.Bool
	nextSequence      uint16
	sequenceStarted   bool

	// Adaptive mode (adaptive.go). ssrc is fixed before workers start;
	// sample is owned by the reconcile worker; the rest are shared atomics.
	ssrc       uint32
	sample     rateSample
	limitAt    time.Time                     // last network signal of a limit; owned by the reconcile worker
	lossBase   lossBase                      // owned by the reconcile worker
	rate       atomic.Int64                  // bps estimate, 0 until there is evidence of a limit
	rtxRate    atomic.Int64                  // bps of NACK resends over the last second
	rr         atomic.Pointer[receiverStats] // latest RR for the primary SSRC
	lastLoss   atomic.Int32                  // last interval loss in permille + 1, 0 = none yet
	remb       atomic.Int64                  // bps from the viewer's own REMB, if it sends one
	rembAt     atomic.Int64                  // unix nanoseconds of that REMB
	congestion atomic.Uint64                 // queue overflows and frames that went stale in the queue
	slow       atomic.Bool                   // excluded from the source ceiling

	// Keyframe backlog, guarded by queueMu. keysPending counts keyframes queued
	// or being written. Frames are numbered by queuedTotal on enqueue and by
	// takenTotal on dequeue or discard; a frame queued while keysPending > 0
	// waits for that keyframe's write, so numbers up to backlogUntil get the
	// keyframe age budget. Frames queued after the write keep the normal one.
	keysPending  int
	queuedTotal  uint64
	takenTotal   uint64
	backlogUntil uint64

	lastOutBytes   uint64
	lastOutPackets uint64
}

type PublishResult struct {
	StreamID domain.StreamID
	Answer   mediasignal.SessionDescription
}

type SubscribeResult struct {
	Answer mediasignal.SessionDescription
}

func (m *Manager) Publish(ctx context.Context, ownerID uint64, offer mediasignal.SessionDescription) (PublishResult, error) {
	if !m.sessionInChannel(ownerID) {
		return PublishResult{}, voice.ErrSessionNotInChannel
	}
	m.mu.Lock()
	if isClosed(m.done) {
		m.mu.Unlock()
		return PublishResult{}, errors.New("media manager closed")
	}
	if _, exists := m.ownerStreams[ownerID]; exists {
		m.mu.Unlock()
		return PublishResult{}, ErrPublisherExists
	}
	if err := m.checkPeerLimitLocked(0, 0); err != nil {
		m.mu.Unlock()
		return PublishResult{}, err
	}
	streamID, err := newStreamID(m.publishers)
	if err != nil {
		m.mu.Unlock()
		return PublishResult{}, err
	}
	pc, err := m.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		m.mu.Unlock()
		return PublishResult{}, err
	}
	p := &publisher{id: streamID, ownerID: ownerID, pc: pc, hasAudio: offerHasOpus(offer.SDP, true), subscribers: make(map[string]*subscriber), closed: make(chan struct{}), createdAt: time.Now(), recovery: make(chan struct{}, 1)}

	m.publishers[streamID] = p
	m.ownerStreams[ownerID] = streamID
	m.mu.Unlock()
	m.startWorker(func() { runRecovery(p) })

	cleanup := func() { m.StopPublisher(ownerID, streamID) }
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("screen publisher state: stream=%d owner=%d state=%s", streamID, ownerID, state)
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			cleanup()
		}
	})
	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if remote.Kind() == webrtc.RTPCodecTypeAudio {
			// Sound is optional: an unexpected audio track is ignored, never fatal.
			p.mu.Lock()
			start := p.hasAudio && !p.audioStarted && strings.EqualFold(remote.Codec().MimeType, webrtc.MimeTypeOpus)
			p.audioStarted = p.audioStarted || start
			p.mu.Unlock()
			if start {
				log.Printf("screen publisher audio track: stream=%d owner=%d ssrc=%d", streamID, ownerID, remote.SSRC())
				m.startWorker(func() { m.forwardAudio(p, remote) })
			}
			return
		}
		if !supportedScreenCodec(remote.Codec().RTPCodecCapability) {
			cleanup()
			return
		}
		p.mu.Lock()
		if p.ready || p.starting {
			p.mu.Unlock()
			return
		}
		p.codec = remote.Codec().RTPCodecCapability
		p.ssrc = remote.SSRC()
		p.starting = true
		p.mu.Unlock()
		log.Printf("screen publisher track: stream=%d owner=%d codec=%s ssrc=%d", streamID, ownerID, remote.Codec().MimeType, remote.SSRC())
		// Serialize Hub publication with removal from the manager to prevent
		// a late OnTrack callback from resurrecting metadata after StopPublisher.
		m.mu.Lock()
		if m.publishers[streamID] != p || isClosed(m.done) {
			m.mu.Unlock()
			return
		}
		_, startErr := m.hub.StartScreenShare(ownerID, streamID)
		m.mu.Unlock()
		if startErr != nil {
			cleanup()
			return
		}
		p.mu.Lock()
		if isClosed(p.closed) {
			p.mu.Unlock()
			return
		}
		p.ready = true
		p.starting = false
		p.mu.Unlock()
		m.startWorker(func() { m.forward(p, remote) })
	})
	if _, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		cleanup()
		return PublishResult{}, err
	}
	if p.hasAudio {
		if _, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			cleanup()
			return PublishResult{}, err
		}
	}
	answer, err := acceptOffer(ctx, pc, offer)
	if err != nil {
		cleanup()
		return PublishResult{}, err
	}
	return PublishResult{StreamID: streamID, Answer: answer}, nil
}

func (m *Manager) Subscribe(ctx context.Context, sessionID uint64, streamID domain.StreamID, subscriberID string, offer mediasignal.SessionDescription) (SubscribeResult, error) {
	if subscriberID == "" {
		return SubscribeResult{}, errors.New("subscriber ID is required")
	}
	if !m.hub.CanSubscribeScreen(sessionID, streamID) {
		return SubscribeResult{}, ErrSubscriptionDenied
	}
	m.mu.RLock()
	p := m.publishers[streamID]
	limitErr := m.checkPeerLimitLocked(sessionID, streamID)
	m.mu.RUnlock()
	if p == nil {
		return SubscribeResult{}, ErrStreamNotReady
	}
	if limitErr != nil {
		return SubscribeResult{}, limitErr
	}
	p.mu.Lock()
	if !p.ready || isClosed(p.closed) {
		p.mu.Unlock()
		return SubscribeResult{}, ErrStreamNotReady
	}
	if _, exists := p.subscribers[subscriberID]; exists {
		p.mu.Unlock()
		return SubscribeResult{}, errors.New("subscriber already exists")
	}
	codec := p.codec
	p.mu.Unlock()
	if !offerSupportsCodec(offer.SDP, codec) {
		return SubscribeResult{}, &publicError{message: fmt.Sprintf("viewer does not support stream codec/profile %s (%s); ask the sender to restart with VP8", codec.MimeType, codec.SDPFmtpLine)}
	}
	pc, err := m.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return SubscribeResult{}, err
	}
	track, err := webrtc.NewTrackLocalStaticRTP(codec, "screen", fmt.Sprintf("screen-%d", streamID))
	if err != nil {
		_ = pc.Close()
		return SubscribeResult{}, err
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		_ = pc.Close()
		return SubscribeResult{}, err
	}
	// Same stream id as the video, so the viewer synchronizes both tracks.
	var audioTrack *webrtc.TrackLocalStaticRTP
	var audioSender *webrtc.RTPSender
	if p.hasAudio && offerHasOpus(offer.SDP, false) {
		if audioTrack, err = webrtc.NewTrackLocalStaticRTP(screenAudioCodec().RTPCodecCapability, "screen-audio", fmt.Sprintf("screen-%d", streamID)); err == nil {
			audioSender, err = pc.AddTrack(audioTrack)
		}
		if err != nil {
			_ = pc.Close()
			return SubscribeResult{}, err
		}
	}
	// The early check avoids building a connection that cannot be admitted, but
	// parallel requests all pass it. Exclusive m.mu makes this one exact.
	m.mu.Lock()
	if err := m.checkPeerLimitLocked(sessionID, streamID); err != nil {
		m.mu.Unlock()
		_ = pc.Close()
		return SubscribeResult{}, err
	}
	p.mu.Lock()
	if !p.ready || isClosed(p.closed) || p.subscribers[subscriberID] != nil {
		p.mu.Unlock()
		m.mu.Unlock()
		_ = pc.Close()
		return SubscribeResult{}, ErrStreamNotReady
	}
	s := &subscriber{id: subscriberID, sessionID: sessionID, pc: pc, track: track, sender: sender, audioTrack: audioTrack, packets: make(chan *videoFrame, backlogQueueSize), waitingKeyframe: true, wake: make(chan struct{}, 1), closed: make(chan struct{}), closingGauge: &m.closing}
	if encodings := sender.GetParameters().Encodings; len(encodings) > 0 {
		s.ssrc = uint32(encodings[0].SSRC)
	}
	p.subscribers[subscriberID] = s
	p.mu.Unlock()
	m.mu.Unlock()
	m.startWorker(func() { drainRTCP(p, s, sender) })
	if audioSender != nil {
		m.startWorker(func() { drainAudioRTCP(audioSender) })
	}
	m.startWorker(func() { m.runSubscriber(p, s) })
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("screen subscriber state: stream=%d session=%d subscriber=%q state=%s", streamID, sessionID, subscriberID, state)
		if state == webrtc.PeerConnectionStateConnected {
			s.connected.Store(true)
			s.recoveryOnce.Do(func() {
				requestRecoveryKeyframe(p, s)
				m.startWorker(func() { requestStartupKeyframes(p, s) })
			})
		}
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			m.removeSubscriber(p, s)
		}
	})
	answer, err := acceptOffer(ctx, pc, offer)
	if err != nil {
		m.removeSubscriber(p, s)
		return SubscribeResult{}, &publicError{message: fmt.Sprintf("viewer cannot negotiate stream codec %s", codec.MimeType), cause: err}
	}
	return SubscribeResult{Answer: answer}, nil
}

func (m *Manager) Unsubscribe(sessionID uint64, streamID domain.StreamID, subscriberID string) {
	m.mu.RLock()
	p := m.publishers[streamID]
	m.mu.RUnlock()
	if p == nil {
		return
	}
	p.mu.RLock()
	s := p.subscribers[subscriberID]
	p.mu.RUnlock()
	if s != nil && s.sessionID == sessionID {
		m.removeSubscriber(p, s)
	}
}

func (m *Manager) removeSubscriber(p *publisher, s *subscriber) {
	p.mu.Lock()
	if p.subscribers[s.id] == s {
		delete(p.subscribers, s.id)
	}
	p.mu.Unlock()
	s.close()
}

func (m *Manager) StopPublisher(ownerID uint64, streamID domain.StreamID) {
	m.mu.Lock()
	p := m.publishers[streamID]
	if p == nil || p.ownerID != ownerID {
		m.mu.Unlock()
		return
	}
	delete(m.publishers, streamID)
	delete(m.ownerStreams, ownerID)
	stopErr := m.hub.StopScreenShare(ownerID, streamID)
	m.mu.Unlock()
	p.close()
	if err := stopErr; err != nil {
		log.Printf("stop screen state failed: stream_id=%d owner_session_id=%d error=%v", streamID, ownerID, err)
	}
	log.Printf("screen publisher stopped: stream_id=%d owner_session_id=%d", streamID, ownerID)
}

func (m *Manager) Close() {
	m.mu.Lock()
	m.closeOnce.Do(func() { close(m.done) })
	m.mu.Unlock()
	m.mu.RLock()
	ids := make([]domain.StreamID, 0, len(m.publishers))
	owners := make([]uint64, 0, len(m.publishers))
	for id, p := range m.publishers {
		ids = append(ids, id)
		owners = append(owners, p.ownerID)
	}
	m.mu.RUnlock()
	for i := range ids {
		m.StopPublisher(owners[i], ids[i])
	}
	m.workers.Wait()
}

func (m *Manager) reconcileLoop() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	ticks := 0
	for {
		select {
		case <-m.done:
			return
		case <-ticker.C:
			m.reconcile()
			ticks++
			if ticks%10 == 0 {
				m.logMetrics()
			}
		}
	}
}

func (m *Manager) reconcile() {
	m.mu.RLock()
	publishers := make([]*publisher, 0, len(m.publishers))
	for _, p := range m.publishers {
		publishers = append(publishers, p)
	}
	m.mu.RUnlock()
	for _, p := range publishers {
		p.mu.RLock()
		ready := p.ready
		p.mu.RUnlock()
		if !ready {
			if time.Since(p.createdAt) > 30*time.Second {
				m.StopPublisher(p.ownerID, p.id)
			}
			continue
		}
		stream, ok := m.hub.ScreenStream(p.id)
		if !ok || stream.OwnerSessionID != p.ownerID {
			m.StopPublisher(p.ownerID, p.id)
			continue
		}
		p.mu.RLock()
		subscribers := make([]*subscriber, 0, len(p.subscribers))
		for _, s := range p.subscribers {
			subscribers = append(subscribers, s)
		}
		p.mu.RUnlock()
		allowed := subscribers[:0]
		for _, s := range subscribers {
			if !m.hub.CanSubscribeScreen(s.sessionID, p.id) {
				m.removeSubscriber(p, s)
				continue
			}
			allowed = append(allowed, s)
		}
		if m.adaptive {
			m.adaptRates(p, allowed, time.Now())
		}
	}
}

func (m *Manager) sessionInChannel(id uint64) bool {
	session, ok := m.hub.Get(id)
	return ok && session.ChannelID != 0
}

// Add is serialized with closing done, so Wait cannot race new workers.
// Never invoke this while holding Manager.mu. Workers stop through done or the
// publisher/subscriber signal plus PeerConnection.Close to unblock Pion I/O.
func (m *Manager) startWorker(run func()) {
	m.mu.RLock()
	if isClosed(m.done) {
		m.mu.RUnlock()
		return
	}
	m.workers.Add(1)
	m.mu.RUnlock()
	go func() { defer m.workers.Done(); run() }()
}

// checkPeerLimitLocked reports whether another peer connection fits under the
// server limit and, for a viewer, under its per-session limits. The caller
// holds m.mu; each publisher's lock is taken in turn.
func (m *Manager) checkPeerLimitLocked(viewerID uint64, streamID domain.StreamID) error {
	peers := len(m.publishers)
	viewerTotal, viewerStream := 0, 0
	for id, p := range m.publishers {
		p.mu.RLock()
		peers += len(p.subscribers)
		if viewerID != 0 {
			for _, s := range p.subscribers {
				if s.sessionID != viewerID {
					continue
				}
				viewerTotal++
				if id == streamID {
					viewerStream++
				}
			}
		}
		p.mu.RUnlock()
	}
	switch {
	case peers >= m.maxPeers:
		return fmt.Errorf("%w: server has %d screen connections", ErrMediaLimit, peers)
	case viewerStream >= maxSessionStreamSubscriptions:
		return fmt.Errorf("%w: %d connections to this stream", ErrMediaLimit, viewerStream)
	case viewerTotal >= maxSessionSubscriptions:
		return fmt.Errorf("%w: %d screen subscriptions", ErrMediaLimit, viewerTotal)
	}
	return nil
}
