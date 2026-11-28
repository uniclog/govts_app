package media

import (
	"sync"
	"testing"
	"time"

	"github.com/pion/rtp"
)

func queuedSubscriber() (*publisher, *subscriber) {
	p := &publisher{closed: make(chan struct{}), recovery: make(chan struct{}, 1)}
	s := &subscriber{closed: make(chan struct{}), packets: make(chan *videoFrame, subscriberQueueSize), wake: make(chan struct{}, 1), waitingKeyframe: true}
	s.connected.Store(true)
	return p, s
}

func sampleFrame(key bool) *videoFrame {
	packet := &rtp.Packet{Header: rtp.Header{Version: 2, Marker: true}, Payload: []byte{0x10, 0}}
	return &videoFrame{packets: []*rtp.Packet{packet}, at: time.Now(), bytes: packet.MarshalSize(), key: key}
}

func TestQueueRejectsExpiredOrOversizedKeyframes(t *testing.T) {
	for _, kind := range []string{"expired", "bytes", "packets"} {
		t.Run(kind, func(t *testing.T) {
			p, s := queuedSubscriber()
			frame := sampleFrame(true)
			switch kind {
			case "expired":
				frame.at = time.Now().Add(-2 * keyFrameMaxAge)
			case "bytes":
				frame.bytes = maxFrameBytes + 1
			case "packets":
				frame.packets = make([]*rtp.Packet, maxFramePackets+1)
			}
			s.enqueue(p, frame)
			if len(s.packets) != 0 || !s.waitingKeyframe || s.queuedBytes != 0 || s.queuedPackets != 0 {
				t.Fatalf("invalid keyframe accepted: frames=%d waiting=%v bytes=%d packets=%d", len(s.packets), s.waitingKeyframe, s.queuedBytes, s.queuedPackets)
			}
		})
	}
}

func TestQueueDropsDependenciesAndResumesOnKeyframe(t *testing.T) {
	p, s := queuedSubscriber()
	s.enqueue(p, sampleFrame(false))
	if len(s.packets) != 0 {
		t.Fatal("new viewer received a delta frame before its keyframe")
	}
	key := sampleFrame(true)
	s.enqueue(p, key)
	if got := <-s.packets; got != key {
		t.Fatal("queue deep-copied shared immutable frame")
	}
	s.queuedBytes, s.queuedPackets = 0, 0
	for i := 0; i <= subscriberQueueSize; i++ {
		s.enqueue(p, sampleFrame(false))
	}
	if len(s.packets) != 0 || !s.waitingKeyframe {
		t.Fatal("overflow retained dependent video")
	}
	s.enqueue(p, sampleFrame(false))
	s.enqueue(p, sampleFrame(true))
	if len(s.packets) != 1 || s.waitingKeyframe {
		t.Fatal("viewer failed to recover on a fresh keyframe")
	}
}

func TestQueueCancellationConcurrentWithProducer(t *testing.T) {
	p, s := queuedSubscriber()
	frame := sampleFrame(true)
	var producers sync.WaitGroup
	for range 4 {
		producers.Go(func() {
			for range 1000 {
				s.enqueue(p, frame)
			}
		})
	}
	close(s.closed)
	s.queueMu.Lock()
	s.discardQueue()
	s.queueMu.Unlock()
	producers.Wait()
	if len(s.packets) != 0 || s.queuedBytes != 0 || s.queuedPackets != 0 {
		t.Fatal("closed subscriber retained queued payloads")
	}
}

func TestFrameStartRejectsMalformedPayloads(t *testing.T) {
	for _, test := range []struct {
		name, codec string
		payload     []byte
	}{
		{"VP8 descriptor only", "video/VP8", []byte{0x10}},
		{"VP9 descriptor only", "video/VP9", []byte{0x08}},
		{"AV1 header only", "video/AV1", []byte{0x08}},
		{"AV1 continuation sequence", "video/AV1", []byte{0x98, 0x08}},
		{"AV1 truncated OBU", "video/AV1", []byte{0x08, 0x7f, 0x08}},
		{"AV1 missing last OBU", "video/AV1", []byte{0x28, 1, 0x08}},
		{"H264 IDR header only", "video/H264", []byte{0x65}},
		{"H264 empty FU-A", "video/H264", []byte{0x7c, 0x85}},
		{"H264 truncated STAP-A", "video/H264", []byte{0x78, 0, 2, 0x65}},
		{"H264 trailing STAP-A byte", "video/H264", []byte{0x78, 0, 2, 0x65, 0x80, 0}},
	} {
		t.Run(test.name, func(t *testing.T) {
			start, key, _ := frameMetadata(test.codec, test.payload)
			if start || key {
				t.Fatalf("malformed payload recognized as start=%v key=%v", start, key)
			}
		})
	}
}

func FuzzFrameStart(f *testing.F) {
	for codec := byte(0); codec < 4; codec++ {
		for _, payload := range [][]byte{nil, {0x10}, {0x10, 0}, {0x18, 0x08, 0x80}, {0x7c, 0x85, 0x80}, {0x78, 0, 2, 0x65, 0x80}} {
			f.Add(codec, payload)
		}
	}
	f.Fuzz(func(t *testing.T, codec byte, payload []byte) {
		names := [...]string{"video/VP8", "video/VP9", "video/AV1", "video/H264"}
		start, key, _ := frameMetadata(names[codec%4], payload)
		if key && !start {
			t.Fatal("continuation packet identified as a keyframe start")
		}
	})
}

func TestFrameStartsForSupportedCodecs(t *testing.T) {
	for _, test := range []struct {
		codec   string
		payload []byte
		key     bool
	}{
		{"video/VP8", []byte{0x10, 0x00}, true},
		{"video/VP8", []byte{0x10, 0x01}, false},
		{"video/VP9", []byte{0x08, 0x01}, true},
		{"video/VP9", []byte{0x48, 0x01}, false},
		{"video/AV1", []byte{0x18, 0x08, 0x80}, true},
		{"video/AV1", []byte{0x10, 0x30, 0x80}, false},
		{"video/H264", []byte{0x65, 0x80}, true},
		{"video/H264", []byte{0x41, 0x80}, false},
		{"video/H264", []byte{0x7c, 0x85, 0x80}, true},
		{"video/H264", []byte{0x78, 0, 2, 0x67, 0x80, 0, 2, 0x65, 0x80}, true},
	} {
		start, key, _ := frameMetadata(test.codec, test.payload)
		if !start || key != test.key {
			t.Errorf("%s %x: start=%v key=%v, want key=%v", test.codec, test.payload, start, key, test.key)
		}
	}
}

func TestFrameMetadataDistinguishesContinuationFromMalformedPacket(t *testing.T) {
	start, key, valid := frameMetadata("video/VP8", []byte{0x00, 0x01})
	if start || key || !valid {
		t.Fatal("ordinary continuation rejected")
	}
	start, key, valid = frameMetadata("video/VP8", []byte{0x80})
	if start || key || valid {
		t.Fatal("truncated descriptor accepted as a continuation")
	}
}

func TestVP9EnhancementLayerCannotResumeAWholePicture(t *testing.T) {
	// B=1, L=1, SID=1, F=0, TL0PICIDX=0, followed by frame payload.
	start, key, valid := frameMetadata("video/VP9", []byte{0x28, 0x02, 0, 1})
	if start || key || !valid {
		t.Fatalf("enhancement layer: start=%v key=%v valid=%v", start, key, valid)
	}
}
