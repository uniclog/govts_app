package media

import (
	"sync"
	"sync/atomic"

	"github.com/pion/interceptor"
	"github.com/pion/rtp"
)

// Registered inside the NACK responder: both original writes and cached resends
// traverse this observer. It measures RTP bytes before SRTP/UDP/IP overhead.
type transportKey struct {
	connection string
	ssrc       uint32
}
type transportMeters struct{ streams sync.Map }
type transportMeter struct {
	mu          sync.Mutex
	seen        [nackResponderCache]uint16
	valid       [nackResponderCache]bool
	repeats     atomic.Uint64
	repeatBytes atomic.Uint64
}
type transportObserver struct {
	interceptor.NoOp
	owner      *transportMeters
	connection string
	mu         sync.Mutex
	keys       []uint32
	closed     bool
}

func (f *transportMeters) NewInterceptor(id string) (interceptor.Interceptor, error) {
	return &transportObserver{owner: f, connection: id}, nil
}
func (o *transportObserver) BindLocalStream(info *interceptor.StreamInfo, writer interceptor.RTPWriter) interceptor.RTPWriter {
	meter := &transportMeter{}
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return writer
	}
	o.owner.streams.Store(transportKey{o.connection, info.SSRC}, meter)
	o.keys = append(o.keys, info.SSRC)
	o.mu.Unlock()
	return interceptor.RTPWriterFunc(func(header *rtp.Header, payload []byte, attributes interceptor.Attributes) (int, error) {
		meter.mu.Lock()
		slot := int(header.SequenceNumber) % nackResponderCache
		repeat := header.SSRC != info.SSRC || (meter.valid[slot] && meter.seen[slot] == header.SequenceNumber)
		if header.SSRC == info.SSRC {
			meter.seen[slot] = header.SequenceNumber
			meter.valid[slot] = true
		}
		meter.mu.Unlock()
		n, err := writer.Write(header, payload, attributes)
		if err == nil && repeat {
			meter.repeats.Add(1)
			meter.repeatBytes.Add(uint64(header.MarshalSize() + len(payload)))
		}
		return n, err
	})
}
func (o *transportObserver) UnbindLocalStream(info *interceptor.StreamInfo) {
	o.owner.streams.Delete(transportKey{o.connection, info.SSRC})
}

func (o *transportObserver) Close() error {
	o.mu.Lock()
	o.closed = true
	keys := o.keys
	o.keys = nil
	o.mu.Unlock()
	for _, key := range keys {
		o.owner.streams.Delete(transportKey{o.connection, key})
	}
	return nil
}
