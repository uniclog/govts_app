package media

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/mediasignal"
	"uniclog.io/sonoryx/internal/voice"
)

var (
	ErrPublisherExists    = errors.New("publisher already exists")
	ErrStreamNotReady     = errors.New("screen stream is not ready")
	ErrSubscriptionDenied = errors.New("screen stream subscription denied")
)

const (
	subscriberQueueSize = 256
	nackResponderCache  = 2048
	recoveryPLIInterval = time.Second
	startupPLIRequests  = 5
)

type Manager struct {
	mu           sync.RWMutex
	hub          *voice.Hub
	api          *webrtc.API
	publishers   map[domain.StreamID]*publisher
	ownerStreams map[uint64]domain.StreamID
	done         chan struct{}
	closeOnce    sync.Once
}

type Config struct {
	MinUDPPort   uint16
	MaxUDPPort   uint16
	AdvertisedIP string
}

type publisher struct {
	id            domain.StreamID
	ownerID       uint64
	pc            *webrtc.PeerConnection
	mu            sync.RWMutex
	codec         webrtc.RTPCodecCapability
	ssrc          webrtc.SSRC
	ready         bool
	starting      bool
	subscribers   map[string]*subscriber
	closed        chan struct{}
	closeOnce     sync.Once
	createdAt     time.Time
	inBytes       atomic.Uint64
	inPackets     atomic.Uint64
	metricsAt     time.Time
	lastInBytes   uint64
	lastInPackets uint64
}

type subscriber struct {
	id              string
	sessionID       uint64
	pc              *webrtc.PeerConnection
	track           *webrtc.TrackLocalStaticRTP
	packets         chan *rtp.Packet
	closed          chan struct{}
	closeOnce       sync.Once
	outBytes        atomic.Uint64
	outPackets      atomic.Uint64
	drops           atomic.Uint64
	plis            atomic.Uint64
	nacks           atomic.Uint64
	recoveryOnce    sync.Once
	lastRecoveryPLI atomic.Int64
	lastOutBytes    uint64
	lastOutPackets  uint64
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
	if _, exists := m.ownerStreams[ownerID]; exists {
		m.mu.Unlock()
		return PublishResult{}, ErrPublisherExists
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
	p := &publisher{id: streamID, ownerID: ownerID, pc: pc, subscribers: make(map[string]*subscriber), closed: make(chan struct{}), createdAt: time.Now()}
	m.publishers[streamID] = p
	m.ownerStreams[ownerID] = streamID
	m.mu.Unlock()

	cleanup := func() { m.StopPublisher(ownerID, streamID) }
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("screen publisher state: stream=%d owner=%d state=%s", streamID, ownerID, state)
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			cleanup()
		}
	})
	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
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
		if _, startErr := m.hub.StartScreenShare(ownerID, streamID); startErr != nil {
			cleanup()
			return
		}
		p.mu.Lock()
		p.ready = true
		p.starting = false
		p.mu.Unlock()
		go m.forward(p, remote)
	})
	if _, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		cleanup()
		return PublishResult{}, err
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
	m.mu.RUnlock()
	if p == nil {
		return SubscribeResult{}, ErrStreamNotReady
	}
	p.mu.Lock()
	if !p.ready {
		p.mu.Unlock()
		return SubscribeResult{}, ErrStreamNotReady
	}
	if _, exists := p.subscribers[subscriberID]; exists {
		p.mu.Unlock()
		return SubscribeResult{}, errors.New("subscriber already exists")
	}
	pc, err := m.api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		p.mu.Unlock()
		return SubscribeResult{}, err
	}
	track, err := webrtc.NewTrackLocalStaticRTP(p.codec, "screen", fmt.Sprintf("screen-%d", streamID))
	if err != nil {
		p.mu.Unlock()
		_ = pc.Close()
		return SubscribeResult{}, err
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		p.mu.Unlock()
		_ = pc.Close()
		return SubscribeResult{}, err
	}
	s := &subscriber{id: subscriberID, sessionID: sessionID, pc: pc, track: track, packets: make(chan *rtp.Packet, subscriberQueueSize), closed: make(chan struct{})}
	p.subscribers[subscriberID] = s
	p.mu.Unlock()
	go drainRTCP(p, s, sender)
	go m.runSubscriber(p, s)
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		log.Printf("screen subscriber state: stream=%d session=%d subscriber=%q state=%s", streamID, sessionID, subscriberID, state)
		if state == webrtc.PeerConnectionStateConnected {
			s.recoveryOnce.Do(func() {
				requestRecoveryKeyframe(p, s)
				go requestStartupKeyframes(p, s)
			})
		}
		if state == webrtc.PeerConnectionStateFailed || state == webrtc.PeerConnectionStateClosed {
			m.Unsubscribe(sessionID, streamID, subscriberID)
		}
	})
	answer, err := acceptOffer(ctx, pc, offer)
	if err != nil {
		m.Unsubscribe(sessionID, streamID, subscriberID)
		return SubscribeResult{}, err
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
	p.mu.Lock()
	s := p.subscribers[subscriberID]
	if s != nil && s.sessionID == sessionID {
		delete(p.subscribers, subscriberID)
	} else {
		s = nil
	}
	p.mu.Unlock()
	if s != nil {
		s.close()
	}
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
	m.mu.Unlock()
	p.close()
	if err := m.hub.StopScreenShare(ownerID, streamID); err != nil {
		log.Printf("stop screen state failed: stream_id=%d owner_session_id=%d error=%v", streamID, ownerID, err)
	}
	log.Printf("screen publisher stopped: stream_id=%d owner_session_id=%d", streamID, ownerID)
}

func (m *Manager) Close() {
	m.closeOnce.Do(func() { close(m.done) })
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
		invalid := make([]*subscriber, 0)
		for _, s := range p.subscribers {
			if !m.hub.CanSubscribeScreen(s.sessionID, p.id) {
				invalid = append(invalid, s)
			}
		}
		p.mu.RUnlock()
		for _, s := range invalid {
			m.Unsubscribe(s.sessionID, p.id, s.id)
		}
	}
}

func (m *Manager) sessionInChannel(id uint64) bool {
	session, ok := m.hub.Get(id)
	return ok && session.ChannelID != 0
}
