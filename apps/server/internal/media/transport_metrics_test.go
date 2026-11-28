package media

import (
	"testing"
	"time"

	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/nack"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

func TestTransportObserverIsolatesConnectionsAndSequenceWrap(t *testing.T) {
	factory := &transportMeters{}
	first, _ := factory.NewInterceptor("one")
	second, _ := factory.NewInterceptor("two")
	defer first.Close()
	defer second.Close()
	info := &interceptor.StreamInfo{SSRC: 123}
	sink := interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) { return 1, nil })
	a, b := first.BindLocalStream(info, sink), second.BindLocalStream(info, sink)
	payload := []byte{1}
	for i := 0; i < 65538; i++ {
		if _, err := a.Write(&rtp.Header{Version: 2, SSRC: 123, SequenceNumber: uint16(i)}, payload, nil); err != nil {
			t.Fatal(err)
		}
	}
	a.Write(&rtp.Header{Version: 2, SSRC: 123, SequenceNumber: 1}, payload, nil)
	b.Write(&rtp.Header{Version: 2, SSRC: 123, SequenceNumber: 1}, payload, nil)
	for key, want := range map[transportKey]uint64{{"one", 123}: 1, {"two", 123}: 0} {
		value, ok := factory.streams.Load(key)
		if !ok || value.(*transportMeter).repeats.Load() != want {
			t.Fatalf("wrong retransmission count for %v", key)
		}
	}
	first.UnbindLocalStream(info)
	if _, ok := factory.streams.Load(transportKey{"one", 123}); ok {
		t.Fatal("unbinding retained the transport meter")
	}
}

func TestClosedTransportObserverDoesNotRetainLateBinding(t *testing.T) {
	factory := &transportMeters{}
	observer, _ := factory.NewInterceptor("closed")
	observer.Close()
	observer.BindLocalStream(&interceptor.StreamInfo{SSRC: 7}, interceptor.RTPWriterFunc(func(*rtp.Header, []byte, interceptor.Attributes) (int, error) { return 0, nil }))
	if _, ok := factory.streams.Load(transportKey{"closed", 7}); ok {
		t.Fatal("late SDP binding recreated a meter after closing the connection")
	}
}

func TestNACKCacheResendsIndependentPayloadThroughObserver(t *testing.T) {
	for _, rtx := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-SSRC", true: "RTX"}[rtx], func(t *testing.T) {
			factory := &transportMeters{}
			observer, _ := factory.NewInterceptor("viewer")
			defer observer.Close()
			responderFactory, err := nack.NewResponderInterceptor(nack.ResponderSize(nackResponderCache))
			if err != nil {
				t.Fatal(err)
			}
			responder, err := responderFactory.NewInterceptor("viewer")
			if err != nil {
				t.Fatal(err)
			}
			defer responder.Close()
			info := &interceptor.StreamInfo{SSRC: 123, PayloadType: 96, RTCPFeedback: []interceptor.RTCPFeedback{{Type: "nack"}}}
			if rtx {
				info.SSRCRetransmission, info.PayloadTypeRetransmission = 456, 97
			}
			sent := make(chan *rtp.Packet, 4)
			sink := interceptor.RTPWriterFunc(func(header *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
				packet := (&rtp.Packet{Header: *header, Payload: payload}).Clone()
				sent <- packet
				return packet.MarshalSize(), nil
			})
			writer := responder.BindLocalStream(info, observer.BindLocalStream(info, sink))
			header := &rtp.Header{Version: 2, SSRC: 123, PayloadType: 96, SequenceNumber: 42}
			payload := []byte{1, 2, 3}
			if _, err := writer.Write(header, payload, nil); err != nil {
				t.Fatal(err)
			}
			<-sent
			payload[0], header.SequenceNumber = 99, 99
			feedback, _ := rtcp.Marshal([]rtcp.Packet{&rtcp.TransportLayerNack{MediaSSRC: 123, Nacks: []rtcp.NackPair{{PacketID: 42}}}})
			reader := responder.BindRTCPReader(interceptor.RTCPReaderFunc(func(buffer []byte, attributes interceptor.Attributes) (int, interceptor.Attributes, error) {
				return copy(buffer, feedback), attributes, nil
			}))
			if _, _, err := reader.Read(make([]byte, 1500), nil); err != nil {
				t.Fatal(err)
			}
			select {
			case retransmission := <-sent:
				offset := 0
				if rtx {
					offset = 2
					if retransmission.SSRC != 456 || retransmission.PayloadType != 97 || retransmission.Payload[0] != 0 || retransmission.Payload[1] != 42 {
						t.Fatal("invalid RTX envelope")
					}
				}
				if retransmission.Payload[offset] != 1 {
					t.Fatal("NACK cache retained mutable publisher payload")
				}
			case <-time.After(time.Second):
				t.Fatal("NACK did not trigger retransmission")
			}
			// The sink signals just before the observer records the successful write.
			deadline := time.Now().Add(time.Second)
			for {
				value, _ := factory.streams.Load(transportKey{"viewer", 123})
				if value.(*transportMeter).repeats.Load() == 1 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("actual Pion retransmission bypassed the observer")
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}
