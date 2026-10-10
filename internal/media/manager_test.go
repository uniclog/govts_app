package media

import (
	"context"
	"net"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/mediasignal"
	"uniclog.io/sonoryx/internal/voice"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

func TestPublishAndSelectiveSubscribe(t *testing.T) {
	hub := voice.NewHub()
	for _, session := range []*voice.Session{
		{ID: 10, Name: "alice", Addr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 10010}},
		{ID: 20, Name: "bob", Addr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 10020}},
	} {
		hub.Add(session)
		if err := hub.JoinChannel(session.ID, voice.DefaultChannelID); err != nil {
			t.Fatal(err)
		}
	}
	manager, err := NewManager(hub)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	publisherPC, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer publisherPC.Close()
	local, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8, ClockRate: 90000}, "screen", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = publisherPC.AddTrack(local); err != nil {
		t.Fatal(err)
	}
	publishOffer := gatheredOffer(t, ctx, publisherPC)
	published, err := manager.Publish(ctx, 10, publishOffer)
	if err != nil {
		t.Fatal(err)
	}
	if err = publisherPC.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: published.Answer.SDP}); err != nil {
		t.Fatal(err)
	}

	sequence := uint16(1)
	for !waitUntil(ctx, func() bool { _, ok := hub.ScreenStream(published.StreamID); return ok }) {
		if err = local.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: sequence, Timestamp: uint32(sequence) * 3000, SSRC: 1234, Marker: true}, Payload: []byte{0x10, 0x00}}); err != nil {
			t.Fatal(err)
		}
		sequence++
	}

	viewerPC, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer viewerPC.Close()
	received := make(chan struct{}, 1)
	viewerPC.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if _, _, readErr := track.ReadRTP(); readErr == nil {
			received <- struct{}{}
		}
	})
	if _, err = viewerPC.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	subscribeOffer := gatheredOffer(t, ctx, viewerPC)
	subscribed, err := manager.Subscribe(ctx, 20, published.StreamID, "bob-view", subscribeOffer)
	if err != nil {
		t.Fatal(err)
	}
	if err = viewerPC.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: subscribed.Answer.SDP}); err != nil {
		t.Fatal(err)
	}

	for {
		_ = local.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 96, SequenceNumber: sequence, Timestamp: uint32(sequence) * 3000, SSRC: 1234, Marker: true}, Payload: []byte{0x10, 0x00}})
		sequence++
		select {
		case <-received:
			return
		case <-ctx.Done():
			t.Fatal("viewer did not receive RTP")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func gatheredOffer(t *testing.T, ctx context.Context, pc *webrtc.PeerConnection) mediasignal.SessionDescription {
	t.Helper()
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathering := webrtc.GatheringCompletePromise(pc)
	if err = pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
		t.Fatal("ICE gathering timed out")
	case <-gathering:
	}
	return mediasignal.SessionDescription{Type: "offer", SDP: pc.LocalDescription().SDP}
}

func waitUntil(ctx context.Context, condition func() bool) bool {
	if condition() {
		return true
	}
	select {
	case <-ctx.Done():
		return true
	case <-time.After(20 * time.Millisecond):
		return false
	}
}
