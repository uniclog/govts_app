package media

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"uniclog.io/sonoryx/apps/server/internal/voice"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/mediasignal"
)

type relayFixture struct {
	tb        testing.TB
	manager   *Manager
	hub       *voice.Hub
	stream    domain.StreamID
	publisher *webrtc.PeerConnection
	track     *webrtc.TrackLocalStaticRTP
	codec     webrtc.RTPCodecParameters
	sequence  uint16
	timestamp uint32
	viewers   []<-chan *rtp.Packet
}

func codecPeer(tb testing.TB, codec webrtc.RTPCodecParameters) *webrtc.PeerConnection {
	tb.Helper()
	engine := &webrtc.MediaEngine{}
	if err := engine.RegisterCodec(codec, webrtc.RTPCodecTypeVideo); err != nil {
		tb.Fatal(err)
	}
	registry := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(engine, registry); err != nil {
		tb.Fatal(err)
	}
	settings := webrtc.SettingEngine{}
	settings.SetIncludeLoopbackCandidate(true)
	pc, err := webrtc.NewAPI(webrtc.WithMediaEngine(engine), webrtc.WithInterceptorRegistry(registry), webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { pc.Close() })
	return pc
}

func newRelay(tb testing.TB, codec webrtc.RTPCodecParameters, viewers int) *relayFixture {
	tb.Helper()
	hub := voice.NewHub()
	for id := uint64(10); id <= uint64(10+viewers); id++ {
		hub.Add(&voice.Session{ID: id, Name: fmt.Sprint(id), Addr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 10000 + int(id)}})
		if err := hub.JoinChannel(id, voice.DefaultChannelID); err != nil {
			tb.Fatal(err)
		}
	}
	manager, err := NewManager(hub)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(manager.Close)
	pc := codecPeer(tb, codec)
	track, err := webrtc.NewTrackLocalStaticRTP(codec.RTPCodecCapability, "screen", "publisher")
	if err != nil {
		tb.Fatal(err)
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		tb.Fatal(err)
	}
	go func() {
		for {
			if _, _, err := sender.ReadRTCP(); err != nil {
				return
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	published, err := manager.Publish(ctx, 10, fixtureOffer(tb, ctx, pc))
	if err != nil {
		tb.Fatal(err)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: published.Answer.SDP}); err != nil {
		tb.Fatal(err)
	}
	fixture := &relayFixture{tb: tb, manager: manager, hub: hub, stream: published.StreamID, publisher: pc, track: track, codec: codec}
	fixture.wait(func() bool {
		fixture.writeFrame(true, 1)
		manager.mu.RLock()
		p := manager.publishers[published.StreamID]
		manager.mu.RUnlock()
		if p == nil {
			return false
		}
		p.mu.RLock()
		ready := p.ready
		p.mu.RUnlock()
		return ready
	})
	for i := 0; i < viewers; i++ {
		fixture.addViewer(uint64(11+i), fmt.Sprintf("viewer-%d", i))
	}
	return fixture
}

func fixtureOffer(tb testing.TB, ctx context.Context, pc *webrtc.PeerConnection) mediasignal.SessionDescription {
	tb.Helper()
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		tb.Fatal(err)
	}
	gathering := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(offer); err != nil {
		tb.Fatal(err)
	}
	select {
	case <-gathering:
	case <-ctx.Done():
		tb.Fatal("ICE gathering timed out")
	}
	return mediasignal.SessionDescription{Type: "offer", SDP: pc.LocalDescription().SDP}
}

func (f *relayFixture) addViewer(id uint64, subscriberID string) {
	f.tb.Helper()
	pc := codecPeer(f.tb, f.codec)
	received := make(chan *rtp.Packet, 4096)
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		for {
			packet, _, err := track.ReadRTP()
			if err != nil {
				return
			}
			select {
			case received <- packet:
			default:
			}
		}
	})
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		f.tb.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	result, err := f.manager.Subscribe(ctx, id, f.stream, subscriberID, fixtureOffer(f.tb, ctx, pc))
	if err != nil {
		f.tb.Fatal(err)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: result.Answer.SDP}); err != nil {
		f.tb.Fatal(err)
	}
	f.wait(func() bool {
		f.manager.mu.RLock()
		p := f.manager.publishers[f.stream]
		f.manager.mu.RUnlock()
		if p == nil {
			return false
		}
		p.mu.RLock()
		s := p.subscribers[subscriberID]
		p.mu.RUnlock()
		return pc.ConnectionState() == webrtc.PeerConnectionStateConnected && s != nil && s.connected.Load()
	})
	f.viewers = append(f.viewers, received)
}

func (f *relayFixture) wait(condition func() bool) {
	f.tb.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			f.tb.Fatal("loopback condition timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *relayFixture) packets(key bool, count int) []*rtp.Packet {
	f.timestamp += 3000
	packets := make([]*rtp.Packet, count)
	for i := range packets {
		payload := make([]byte, 1200)
		switch f.codec.MimeType {
		case webrtc.MimeTypeVP8:
			if i == 0 {
				payload[0] = 0x10
			}
			if !key {
				payload[1] = 1
			}
		case webrtc.MimeTypeVP9:
			if i == 0 {
				payload[0] = 0x08
			}
			if !key {
				payload[0] |= 0x40
			}
		case webrtc.MimeTypeAV1:
			payload[0] = 0x10
			if i == 0 && key {
				payload[0] |= 0x08
			}
			payload[1] = 0x08
		case webrtc.MimeTypeH264:
			payload[0] = 0x41
			if key {
				payload[0] = 0x65
			}
			payload[1] = 0x80
		}
		packets[i] = &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: f.sequence, Timestamp: f.timestamp, Marker: i == count-1}, Payload: payload}
		f.sequence++
	}
	return packets
}

func (f *relayFixture) writeFrame(key bool, count int) uint32 {
	for _, packet := range f.packets(key, count) {
		if err := f.track.WriteRTP(packet); err != nil {
			f.tb.Fatal(err)
		}
	}
	return f.timestamp
}

func (f *relayFixture) awaitFrame(viewer int, timestamp uint32) []*rtp.Packet {
	f.tb.Helper()
	var packets []*rtp.Packet
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case packet := <-f.viewers[viewer]:
			if packet.Timestamp != timestamp {
				continue
			}
			packets = append(packets, packet)
			if packet.Marker {
				return packets
			}
		case <-timer.C:
			f.tb.Fatalf("viewer %d did not receive timestamp %d", viewer, timestamp)
		}
	}
}

func TestRelaySupportedCodecs(t *testing.T) {
	codecs := screenCodecs()
	lowLevel := codecs[2]
	lowLevel.SDPFmtpLine = strings.ReplaceAll(lowLevel.SDPFmtpLine, "42e034", "42e01f")
	codecs = append(codecs, lowLevel)
	for _, codec := range codecs {
		if codec.MimeType == webrtc.MimeTypeRTX {
			continue
		}
		t.Run(codec.MimeType, func(t *testing.T) {
			fixture := newRelay(t, codec, 1)
			timestamp := fixture.writeFrame(true, 3)
			packets := fixture.awaitFrame(0, timestamp)
			if len(packets) != 3 {
				t.Fatalf("complete frame contains %d packets", len(packets))
			}
			for i := 1; i < len(packets); i++ {
				if packets[i].SequenceNumber != packets[i-1].SequenceNumber+1 {
					t.Fatal("outgoing sequence has an artificial gap")
				}
			}
		})
	}
}

func TestRelayMissingFragmentRecoversOnFreshKeyframe(t *testing.T) {
	fixture := newRelay(t, screenCodecs()[0], 1)
	fixture.awaitFrame(0, fixture.writeFrame(true, 1))
	broken := fixture.packets(false, 3)
	for _, index := range []int{0, 2} {
		if err := fixture.track.WriteRTP(broken[index]); err != nil {
			t.Fatal(err)
		}
	}
	fixture.writeFrame(false, 2)
	// The missing packet was never cached by the source. Wait past the repair
	// deadline, then provide an independent frame rather than a delta dependency.
	time.Sleep(subscriberQueueAge + 30*time.Millisecond)
	packets := fixture.awaitFrame(0, fixture.writeFrame(true, 2))
	if len(packets) != 2 {
		t.Fatal("recovery keyframe was incomplete")
	}
	fixture.awaitFrame(0, fixture.writeFrame(false, 2))
}

func TestRelayRejectsIncompatibleLateViewer(t *testing.T) {
	fixture := newRelay(t, screenCodecs()[6], 0)
	fixture.hub.Add(&voice.Session{ID: 11, Name: "late", Addr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 10011}})
	if err := fixture.hub.JoinChannel(11, voice.DefaultChannelID); err != nil {
		t.Fatal(err)
	}
	viewer := codecPeer(t, screenCodecs()[0])
	if _, err := viewer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := fixture.manager.Subscribe(ctx, 11, fixture.stream, "legacy", fixtureOffer(t, ctx, viewer))
	if err == nil || !strings.Contains(err.Error(), "does not support stream codec/profile") {
		t.Fatalf("late viewer error=%v", err)
	}
	fixture.manager.mu.RLock()
	p := fixture.manager.publishers[fixture.stream]
	fixture.manager.mu.RUnlock()
	p.mu.RLock()
	count := len(p.subscribers)
	p.mu.RUnlock()
	if count != 0 {
		t.Fatal("failed negotiation retained a subscription")
	}
}

func TestRepeatedRelayShutdownReleasesWorkersAndMeters(t *testing.T) {
	for cycle := range 3 {
		t.Run(fmt.Sprint(cycle), func(t *testing.T) {
			fixture := newRelay(t, screenCodecs()[0], 3)
			timestamp := fixture.writeFrame(true, 3)
			for i := range fixture.viewers {
				fixture.awaitFrame(i, timestamp)
			}
			done := make(chan struct{})
			go func() { fixture.manager.Close(); close(done) }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("Manager.Close left media workers blocked")
			}
			fixture.manager.mu.RLock()
			count := len(fixture.manager.publishers)
			fixture.manager.mu.RUnlock()
			if count != 0 {
				t.Fatal("shutdown retained publishers")
			}
			meters := 0
			fixture.manager.meters.streams.Range(func(_, _ any) bool { meters++; return true })
			if meters != 0 {
				t.Fatalf("shutdown retained %d NACK meters", meters)
			}
		})
	}
}

func TestConcurrentCloseAndSubscribe(t *testing.T) {
	f := newRelay(t, screenCodecs()[0], 0)
	f.hub.Add(&voice.Session{ID: 11, Name: "viewer", Addr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 10011}})
	if err := f.hub.JoinChannel(11, voice.DefaultChannelID); err != nil {
		t.Fatal(err)
	}
	viewer := codecPeer(t, f.codec)
	if _, err := viewer.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	offer := fixtureOffer(t, ctx, viewer)
	start, finished := make(chan struct{}), make(chan struct{}, 3)
	go func() { <-start; f.manager.Subscribe(ctx, 11, f.stream, "concurrent", offer); finished <- struct{}{} }()
	go func() { <-start; f.manager.StopPublisher(10, f.stream); finished <- struct{}{} }()
	go func() { <-start; f.manager.Close(); finished <- struct{}{} }()
	close(start)
	for range 3 {
		select {
		case <-finished:
		case <-ctx.Done():
			t.Fatal("close/subscribe race left a blocked worker or signaling operation")
		}
	}
	f.manager.mu.RLock()
	count := len(f.manager.publishers)
	f.manager.mu.RUnlock()
	if count != 0 {
		t.Fatal("closed manager retained a publisher")
	}
}

func TestRelayReorderedPacketsAndLateDuplicates(t *testing.T) {
	fixture := newRelay(t, screenCodecs()[0], 1)
	fixture.awaitFrame(0, fixture.writeFrame(true, 1))
	packets := fixture.packets(false, 3)
	for _, index := range []int{0, 2, 1, 0} {
		if err := fixture.track.WriteRTP(packets[index]); err != nil {
			t.Fatal(err)
		}
	}
	got := fixture.awaitFrame(0, packets[0].Timestamp)
	if len(got) != 3 {
		t.Fatalf("reordered frame contains %d packets", len(got))
	}
	fixture.awaitFrame(0, fixture.writeFrame(false, 2))
}

func TestRelayMembershipRevocationRemovesSubscription(t *testing.T) {
	fixture := newRelay(t, screenCodecs()[0], 1)
	fixture.awaitFrame(0, fixture.writeFrame(true, 1))
	other, err := fixture.hub.CreateChannel(domain.Channel{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.hub.JoinChannel(11, other.ID); err != nil {
		t.Fatal(err)
	}
	fixture.writeFrame(false, 1)
	fixture.wait(func() bool {
		fixture.manager.mu.RLock()
		p := fixture.manager.publishers[fixture.stream]
		fixture.manager.mu.RUnlock()
		p.mu.RLock()
		count := len(p.subscribers)
		p.mu.RUnlock()
		return count == 0
	})
}

func BenchmarkRelayLoopback(b *testing.B) {
	output := log.Writer()
	log.SetOutput(io.Discard)
	b.Cleanup(func() { log.SetOutput(output) })
	for _, viewers := range []int{1, 3, 5} {
		b.Run(fmt.Sprintf("%d_viewers", viewers), func(b *testing.B) {
			fixture := newRelay(b, screenCodecs()[0], viewers)
			const packetsPerFrame = 18
			timestamp := fixture.writeFrame(true, packetsPerFrame)
			for i := range fixture.viewers {
				fixture.awaitFrame(i, timestamp)
			}
			// Warm the 2048-packet server retransmission cache before timing;
			// otherwise allocation averages depend on the benchmark iteration count.
			for range nackResponderCache/packetsPerFrame + 2 {
				timestamp = fixture.writeFrame(false, packetsPerFrame)
				for i := range fixture.viewers {
					fixture.awaitFrame(i, timestamp)
				}
			}
			b.SetBytes(int64(packetsPerFrame * 1212 * viewers))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				timestamp = fixture.writeFrame(false, packetsPerFrame)
				for i := range fixture.viewers {
					fixture.awaitFrame(i, timestamp)
				}
			}
			b.StopTimer()
			fixture.manager.mu.RLock()
			p := fixture.manager.publishers[fixture.stream]
			fixture.manager.mu.RUnlock()
			p.mu.RLock()
			defer p.mu.RUnlock()
			var drops uint64
			for _, s := range p.subscribers {
				drops += s.drops.Load()
			}
			b.ReportMetric(float64(drops), "dropped_packets")
		})
	}
}
