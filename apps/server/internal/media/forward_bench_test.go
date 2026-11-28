package media

import (
	"fmt"
	"testing"
	"time"

	"github.com/pion/rtp"
)

// Same RTP input and viewer count. Legacy reproduces the old per-viewer deep
// Clone and channel send; shared measures the current complete-frame queue.
// Neither measures encryption, UDP I/O, browser codecs, or voice contention.
func BenchmarkFrameFanout(b *testing.B) {
	const packetsPerFrame = 18 // ~2.6 Mbps at 15 FPS and 1200-byte payloads.
	frame := &videoFrame{key: true}
	for i := range packetsPerFrame {
		p := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: uint16(i), Marker: i == packetsPerFrame-1}, Payload: make([]byte, 1200)}
		frame.packets = append(frame.packets, p)
		frame.bytes += p.MarshalSize()
	}
	for _, viewers := range []int{1, 3, 5} {
		b.Run(fmt.Sprintf("legacy/%d_viewers", viewers), func(b *testing.B) {
			queues := make([]chan *rtp.Packet, viewers)
			for i := range queues {
				queues[i] = make(chan *rtp.Packet, 256)
			}
			b.ReportAllocs()
			b.SetBytes(int64(frame.bytes * viewers))
			b.ResetTimer()
			for range b.N {
				for _, packet := range frame.packets {
					for _, queue := range queues {
						queue <- packet.Clone()
						<-queue
					}
				}
			}
		})
		b.Run(fmt.Sprintf("shared/%d_viewers", viewers), func(b *testing.B) {
			p, _ := queuedSubscriber()
			subscribers := make([]*subscriber, viewers)
			for i := range subscribers {
				_, subscribers[i] = queuedSubscriber()
			}
			b.ReportAllocs()
			b.SetBytes(int64(frame.bytes * viewers))
			b.ResetTimer()
			for range b.N {
				frame.at = time.Now()
				for _, s := range subscribers {
					s.enqueue(p, frame)
					<-s.packets
					s.queuedBytes, s.queuedPackets = 0, 0
					<-s.wake
				}
			}
		})
	}
}
