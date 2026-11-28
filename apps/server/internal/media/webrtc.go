package media

import (
	"context"
	"errors"
	"net/netip"
	"strings"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/webrtc/v4"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/mediasignal"
)

func NewManager(hub screenSessions) (*Manager, error) { return NewManagerWithConfig(hub, Config{}) }

func NewManagerWithConfig(hub screenSessions, config Config) (*Manager, error) {
	if hub == nil {
		return nil, errors.New("hub is required")
	}
	mediaEngine := &webrtc.MediaEngine{}
	for _, codec := range screenCodecs() {
		if err := mediaEngine.RegisterCodec(codec, webrtc.RTPCodecTypeVideo); err != nil {
			return nil, err
		}
	}
	if err := mediaEngine.RegisterCodec(screenAudioCodec(), webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	registry := &interceptor.Registry{}
	meters := &transportMeters{}
	registry.Add(meters)
	if err := webrtc.RegisterDefaultInterceptorsWithOptions(mediaEngine, registry,
		webrtc.WithNackResponderOptions(nack.ResponderSize(nackResponderCache)),
		webrtc.WithNackGeneratorOptions(nack.GeneratorSize(maxFramePackets), nack.GeneratorInterval(nackInterval), nack.GeneratorMaxNacksPerPacket(nackMaxPerPacket)),
	); err != nil {
		return nil, err
	}
	network, err := newBufferedMediaNet()
	if err != nil {
		return nil, err
	}
	settingEngine := webrtc.SettingEngine{}
	settingEngine.SetNet(network)
	// A lite agent only answers the viewer's connectivity checks: it never sends
	// STUN to addresses a client wrote into its SDP.
	settingEngine.SetLite(true)
	maxPeers := defaultMaxPeerConnections
	if config.MinUDPPort != 0 || config.MaxUDPPort != 0 {
		if config.MinUDPPort == 0 || config.MaxUDPPort < config.MinUDPPort {
			return nil, errors.New("invalid media UDP port range")
		}
		if err := settingEngine.SetEphemeralUDPPortRange(config.MinUDPPort, config.MaxUDPPort); err != nil {
			return nil, err
		}
		if ports := int(config.MaxUDPPort-config.MinUDPPort) + 1; ports < maxPeers {
			maxPeers = ports
		}
	}
	if config.AdvertisedIP != "" {
		if err := settingEngine.SetICEAddressRewriteRules(webrtc.ICEAddressRewriteRule{
			External:        []string{config.AdvertisedIP},
			AsCandidateType: webrtc.ICECandidateTypeHost,
			Mode:            webrtc.ICEAddressRewriteReplace,
		}); err != nil {
			return nil, err
		}
	}
	m := &Manager{hub: hub, meters: meters, network: network, adaptive: config.AdaptiveBitrate, maxPeers: maxPeers, api: webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine), webrtc.WithInterceptorRegistry(registry), webrtc.WithSettingEngine(settingEngine)), publishers: make(map[domain.StreamID]*publisher), ownerStreams: make(map[uint64]domain.StreamID), done: make(chan struct{})}
	m.startWorker(m.reconcileLoop)
	return m, nil
}

func acceptOffer(ctx context.Context, pc *webrtc.PeerConnection, offer mediasignal.SessionDescription) (mediasignal.SessionDescription, error) {
	if offer.Type != "offer" || offer.SDP == "" {
		return mediasignal.SessionDescription{}, &publicError{message: "invalid WebRTC offer"}
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: withoutLocalCandidates(offer.SDP)}); err != nil {
		return mediasignal.SessionDescription{}, err
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return mediasignal.SessionDescription{}, err
	}
	gathering := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		return mediasignal.SessionDescription{}, err
	}
	select {
	case <-ctx.Done():
		return mediasignal.SessionDescription{}, ctx.Err()
	case <-gathering:
	}
	local := pc.LocalDescription()
	if local == nil {
		return mediasignal.SessionDescription{}, errors.New("missing local description")
	}
	if strings.Contains(local.SDP, "m=video 0 ") || !strings.Contains(local.SDP, "m=video ") {
		return mediasignal.SessionDescription{}, &publicError{message: "no compatible video codec/profile in the offer"}
	}
	return mediasignal.SessionDescription{Type: "answer", SDP: local.SDP}, nil
}

// withoutLocalCandidates drops viewer ICE candidates on loopback, private and
// link-local addresses, so an offer cannot point the server at its own network.
// The lite agent learns the viewer's real address from its checks instead.
func withoutLocalCandidates(sdp string) string {
	lines := strings.SplitAfter(sdp, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !localCandidate(line) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "")
}

// localCandidate reports whether an "a=candidate:" line names a loopback,
// private, link-local or unspecified IP. Hostnames such as mDNS names are kept.
func localCandidate(line string) bool {
	value, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "a=candidate:")
	if !ok {
		return false
	}
	// foundation component transport priority address port typ ...
	fields := strings.Fields(value)
	if len(fields) < 5 {
		return false
	}
	addr, err := netip.ParseAddr(fields[4])
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() || addr.IsUnspecified()
}
