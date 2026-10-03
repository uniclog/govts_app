package media

import (
	"context"
	"errors"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/webrtc/v4"
	"uniclog.io/govts/internal/domain"
	"uniclog.io/govts/internal/mediasignal"
	"uniclog.io/govts/internal/voice"
)

// screenAudioCodec is the system/window audio format Chromium captures for a display stream.
var screenAudioCodec = webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2, SDPFmtpLine: "minptime=10;useinbandfec=1"}

func NewManager(hub *voice.Hub) (*Manager, error) { return NewManagerWithConfig(hub, Config{}) }

func NewManagerWithConfig(hub *voice.Hub, config Config) (*Manager, error) {
	if hub == nil {
		return nil, errors.New("hub is required")
	}
	mediaEngine := &webrtc.MediaEngine{}
	codec := webrtc.RTPCodecParameters{RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000, RTCPFeedback: []webrtc.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}, {Type: "goog-remb"}}}, PayloadType: 96}
	if err := mediaEngine.RegisterCodec(codec, webrtc.RTPCodecTypeVideo); err != nil {
		return nil, err
	}
	audio := webrtc.RTPCodecParameters{RTPCodecCapability: screenAudioCodec, PayloadType: 111}
	if err := mediaEngine.RegisterCodec(audio, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	registry := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptorsWithOptions(mediaEngine, registry, webrtc.WithNackResponderOptions(nack.ResponderSize(nackResponderCache))); err != nil {
		return nil, err
	}
	settingEngine := webrtc.SettingEngine{}
	if config.MinUDPPort != 0 || config.MaxUDPPort != 0 {
		if config.MinUDPPort == 0 || config.MaxUDPPort < config.MinUDPPort {
			return nil, errors.New("invalid media UDP port range")
		}
		if err := settingEngine.SetEphemeralUDPPortRange(config.MinUDPPort, config.MaxUDPPort); err != nil {
			return nil, err
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
	m := &Manager{hub: hub, api: webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine), webrtc.WithInterceptorRegistry(registry), webrtc.WithSettingEngine(settingEngine)), publishers: make(map[domain.StreamID]*publisher), ownerStreams: make(map[uint64]domain.StreamID), done: make(chan struct{})}
	go m.reconcileLoop()
	return m, nil
}

func acceptOffer(ctx context.Context, pc *webrtc.PeerConnection, offer mediasignal.SessionDescription) (mediasignal.SessionDescription, error) {
	if offer.Type != "offer" || offer.SDP == "" {
		return mediasignal.SessionDescription{}, errors.New("invalid WebRTC offer")
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer.SDP}); err != nil {
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
	return mediasignal.SessionDescription{Type: "answer", SDP: local.SDP}, nil
}
