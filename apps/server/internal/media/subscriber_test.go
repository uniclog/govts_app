package media

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"uniclog.io/sonoryx/apps/server/internal/voice"
)

type testTrackWriter func(*rtp.Header, []byte) (int, error)

func (w testTrackWriter) WriteRTP(header *rtp.Header, payload []byte) (int, error) {
	return w(header, payload)
}
func (w testTrackWriter) Write(data []byte) (int, error) { return len(data), nil }

type testTrackContext struct {
	codec  webrtc.RTPCodecParameters
	writer webrtc.TrackLocalWriter
	id     string
	ssrc   webrtc.SSRC
}

func (c testTrackContext) CodecParameters() []webrtc.RTPCodecParameters {
	return []webrtc.RTPCodecParameters{c.codec}
}
func (c testTrackContext) HeaderExtensions() []webrtc.RTPHeaderExtensionParameter { return nil }
func (c testTrackContext) SSRC() webrtc.SSRC                                      { return c.ssrc }
func (c testTrackContext) SSRCRetransmission() webrtc.SSRC                        { return 0 }
func (c testTrackContext) SSRCForwardErrorCorrection() webrtc.SSRC                { return 0 }
func (c testTrackContext) WriteStream() webrtc.TrackLocalWriter                   { return c.writer }
func (c testTrackContext) ID() string                                             { return c.id }
func (c testTrackContext) RTCPReader() interceptor.RTCPReader                     { return nil }

func boundSubscriber(t *testing.T, f *relayFixture, id string, session uint64, write testTrackWriter) *subscriber {
	t.Helper()
	f.hub.Add(&voice.Session{ID: session, Name: id, Addr: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 10000 + int(session)}})
	if err := f.hub.JoinChannel(session, voice.DefaultChannelID); err != nil {
		t.Fatal(err)
	}
	pc, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	track, err := webrtc.NewTrackLocalStaticRTP(f.codec.RTPCodecCapability, "screen", id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := track.Bind(testTrackContext{codec: f.codec, writer: write, id: id, ssrc: webrtc.SSRC(session)}); err != nil {
		t.Fatal(err)
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		t.Fatal(err)
	}
	_, s := queuedSubscriber()
	s.id = id
	s.sessionID = session
	s.pc = pc
	s.track = track
	s.sender = sender
	s.closingGauge = &f.manager.closing
	f.manager.mu.RLock()
	p := f.manager.publishers[f.stream]
	f.manager.mu.RUnlock()
	p.mu.Lock()
	p.subscribers[id] = s
	p.mu.Unlock()
	f.manager.startWorker(func() { f.manager.runSubscriber(p, s) })
	return s
}

func TestSlowViewerDoesNotBlockOtherViewersAndSharedHeadersStayIndependent(t *testing.T) {
	f := newRelay(t, screenCodecs()[0], 0)
	entered := make(chan struct{}, 1)
	var slow *subscriber
	slow = boundSubscriber(t, f, "slow", 11, func(_ *rtp.Header, _ []byte) (int, error) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-slow.closed
		return 0, errors.New("writer cancelled")
	})
	fastPackets := make(chan *rtp.Packet, 64)
	fast := boundSubscriber(t, f, "fast", 12, func(header *rtp.Header, payload []byte) (int, error) {
		header.CSRC[0] = 999
		header.GetExtension(1)[0] = 99
		fastPackets <- (&rtp.Packet{Header: *header, Payload: payload}).Clone()
		return len(payload), nil
	})
	f.manager.mu.RLock()
	p := f.manager.publishers[f.stream]
	f.manager.mu.RUnlock()
	frame := sampleFrame(true)
	frame.packets[0].CSRC = []uint32{123}
	if err := frame.packets[0].SetExtension(1, []byte{7}); err != nil {
		t.Fatal(err)
	}
	frame.bytes = frame.packets[0].MarshalSize()
	slow.enqueue(p, frame)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("slow writer did not enter transport")
	}
	for i := range 40 {
		frame = sampleFrame(i == 0)
		frame.packets[0].SequenceNumber = uint16(i * 10)
		frame.packets[0].CSRC = []uint32{123}
		if err := frame.packets[0].SetExtension(1, []byte{7}); err != nil {
			t.Fatal(err)
		}
		frame.bytes = frame.packets[0].MarshalSize()
		slow.enqueue(p, frame)
		fast.enqueue(p, frame)
		select {
		case packet := <-fastPackets:
			if packet.SSRC != 12 || packet.SequenceNumber != uint16(i) {
				t.Fatalf("wrong per-viewer header: %+v", packet.Header)
			}
		case <-time.After(time.Second):
			t.Fatal("slow subscriber blocked a healthy subscriber")
		}
		if frame.packets[0].CSRC[0] != 123 || frame.packets[0].GetExtension(1)[0] != 7 {
			t.Fatal("Pion writer mutated a shared packet header")
		}
	}
	if slow.drops.Load() == 0 || fast.drops.Load() != 0 {
		t.Fatalf("slow drops=%d fast drops=%d", slow.drops.Load(), fast.drops.Load())
	}
	f.manager.removeSubscriber(p, slow)
	f.manager.removeSubscriber(p, fast)
}

func TestStaleCallbackDoesNotRemoveReplacementSubscription(t *testing.T) {
	f := newRelay(t, screenCodecs()[0], 0)
	write := testTrackWriter(func(_ *rtp.Header, payload []byte) (int, error) { return len(payload), nil })
	old := boundSubscriber(t, f, "same", 11, write)
	f.manager.mu.RLock()
	p := f.manager.publishers[f.stream]
	f.manager.mu.RUnlock()
	f.manager.removeSubscriber(p, old)
	replacement := boundSubscriber(t, f, "same", 11, write)
	f.manager.removeSubscriber(p, old)
	p.mu.RLock()
	active := p.subscribers["same"]
	p.mu.RUnlock()
	if active != replacement || isClosed(replacement.closed) {
		t.Fatal("old callback removed or closed a replacement")
	}
	f.manager.removeSubscriber(p, replacement)
}

func TestWriterFailureRemovesSubscriptionAndClearsQueue(t *testing.T) {
	f := newRelay(t, screenCodecs()[0], 0)
	s := boundSubscriber(t, f, "failed", 11, func(*rtp.Header, []byte) (int, error) { return 0, errors.New("transport failed") })
	f.manager.mu.RLock()
	p := f.manager.publishers[f.stream]
	f.manager.mu.RUnlock()
	s.enqueue(p, sampleFrame(true))
	f.wait(func() bool {
		p.mu.RLock()
		_, active := p.subscribers[s.id]
		p.mu.RUnlock()
		return !active && isClosed(s.closed)
	})
	s.queueMu.Lock()
	defer s.queueMu.Unlock()
	if len(s.packets) != 0 || s.queuedBytes != 0 || s.queuedPackets != 0 {
		t.Fatal("failed writer retained its queue")
	}
}
