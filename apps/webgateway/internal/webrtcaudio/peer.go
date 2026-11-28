package webrtcaudio

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pion/logging"
	"github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v4"
)

const maxSDPBytes = 48 << 10

var ErrOffer = errors.New("Ожидается одно двустороннее аудиосоединение WebRTC с Opus")

var opusCapability = webrtc.RTPCodecCapability{
	MimeType: webrtc.MimeTypeOpus, ClockRate: sampleRate, Channels: 2,
	SDPFmtpLine: "minptime=10",
}

type Counters struct {
	Failures       atomic.Uint64
	RTPReceived    atomic.Uint64
	RTPMissing     atomic.Uint64
	PCMDelayMicros atomic.Uint64
	PCMSamples     atomic.Uint64
}

type Peer struct{ pc *webrtc.PeerConnection }

func NewPeer(ice []webrtc.ICEServer, portMin, portMax uint16, bridge *Bridge, id uint64, counters *Counters) (*Peer, error) {
	engine := &webrtc.MediaEngine{}
	if err := engine.RegisterCodec(webrtc.RTPCodecParameters{RTPCodecCapability: opusCapability, PayloadType: 111}, webrtc.RTPCodecTypeAudio); err != nil {
		return nil, err
	}
	settings := webrtc.SettingEngine{
		LoggerFactory: &logging.DefaultLoggerFactory{Writer: io.Discard, DefaultLogLevel: logging.LogLevelDisabled},
	}
	if portMin != 0 {
		if err := settings.SetEphemeralUDPPortRange(portMin, portMax); err != nil {
			return nil, err
		}
	}
	api := webrtc.NewAPI(webrtc.WithMediaEngine(engine), webrtc.WithSettingEngine(settings))
	pc, err := api.NewPeerConnection(webrtc.Configuration{ICEServers: ice})
	if err != nil {
		return nil, err
	}

	track, err := webrtc.NewTrackLocalStaticRTP(opusCapability, "voice", "sonoryx")
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	transceiver, err := pc.AddTransceiverFromTrack(track, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendrecv})
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	player, err := newOutput(bridge, id, track, counters)
	if err != nil {
		_ = pc.Close()
		return nil, err
	}
	mediaCtx, cancel := context.WithCancel(context.Background())
	go player.run(mediaCtx)
	go func() {
		var buffer [1500]byte
		for {
			if _, _, err := transceiver.Sender().Read(buffer[:]); err != nil {
				return
			}
		}
	}()
	var reading atomic.Bool
	pc.OnTrack(func(remote *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if !strings.EqualFold(remote.Codec().MimeType, webrtc.MimeTypeOpus) || !reading.CompareAndSwap(false, true) {
			return
		}
		bridge.receive(id, remote, counters)
	})
	pc.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateFailed:
			counters.Failures.Add(1)
			cancel()
			_ = pc.Close()
		case webrtc.PeerConnectionStateClosed:
			cancel()
		}
	})
	return &Peer{pc: pc}, nil
}

func ValidateOffer(offer webrtc.SessionDescription) error {
	if offer.Type != webrtc.SDPTypeOffer || len(offer.SDP) > maxSDPBytes {
		return ErrOffer
	}
	var description sdp.SessionDescription
	if err := description.UnmarshalString(offer.SDP); err != nil || len(description.MediaDescriptions) != 1 {
		return ErrOffer
	}
	media := description.MediaDescriptions[0]
	if media.MediaName.Media != "audio" || media.MediaName.Port.Value == 0 {
		return ErrOffer
	}
	for _, attr := range media.Attributes {
		if attr.Key == "inactive" || attr.Key == "sendonly" || attr.Key == "recvonly" {
			return ErrOffer
		}
	}
	for _, attr := range media.Attributes {
		if attr.Key == "rtpmap" && strings.HasSuffix(strings.ToLower(attr.Value), " opus/48000/2") {
			return nil
		}
	}
	return ErrOffer
}

func (p *Peer) Answer(ctx context.Context, offer webrtc.SessionDescription) (webrtc.SessionDescription, error) {
	if err := p.pc.SetRemoteDescription(offer); err != nil {
		return webrtc.SessionDescription{}, ErrOffer
	}
	negotiated := p.pc.GetTransceivers()
	if len(negotiated) != 1 {
		return webrtc.SessionDescription{}, ErrOffer
	}
	answer, err := p.pc.CreateAnswer(nil)
	if err != nil {
		return answer, err
	}
	gathered := webrtc.GatheringCompletePromise(p.pc)
	if err := p.pc.SetLocalDescription(answer); err != nil {
		return answer, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	select {
	case <-ctx.Done():
		return answer, ctx.Err()
	case <-gathered:
		reply := p.pc.LocalDescription()
		if reply == nil {
			return answer, errors.New("missing local description")
		}
		return *reply, nil
	}
}
func (p *Peer) Close() { _ = p.pc.Close() }
