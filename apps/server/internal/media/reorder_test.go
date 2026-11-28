package media

import (
	"slices"
	"testing"
	"time"

	"github.com/pion/rtp"
)

func TestRTPReorderRepairsGapWithoutPLI(t *testing.T) {
	var buffer rtpReorder
	var delivered []uint16
	losses := 0
	deliver := func(item receivedPacket) { delivered = append(delivered, item.packet.SequenceNumber) }
	loss := func() { losses++ }
	now := time.Now()
	for _, sequence := range []uint16{100, 102, 103, 102, 101, 100} {
		buffer.push(&rtp.Packet{Header: rtp.Header{SequenceNumber: sequence}}, now, deliver, loss)
	}
	if !slices.Equal(delivered, []uint16{100, 101, 102, 103}) || losses != 0 || len(buffer.pending) != 0 || !buffer.deadline().IsZero() {
		t.Fatalf("repaired gap failed: delivered=%v losses=%d retained=%d", delivered, losses, len(buffer.pending))
	}
}

func TestRTPReorderWrapsAndExpiresAllUnresolvedPackets(t *testing.T) {
	var buffer rtpReorder
	var delivered []uint16
	losses := 0
	deliver := func(item receivedPacket) { delivered = append(delivered, item.packet.SequenceNumber) }
	loss := func() { losses++ }
	now := time.Now()
	for _, sequence := range []uint16{65534, 1, 0, 65535} {
		buffer.push(&rtp.Packet{Header: rtp.Header{SequenceNumber: sequence}}, now, deliver, loss)
	}
	if !slices.Equal(delivered, []uint16{65534, 65535, 0, 1}) || losses != 0 {
		t.Fatalf("sequence rollover: %v, losses=%d", delivered, losses)
	}
	buffer.push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 3}}, now, deliver, loss)
	buffer.push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 5}}, now, deliver, loss)
	buffer.expire(now.Add(packetRepairAge-time.Nanosecond), deliver, loss)
	if losses != 0 {
		t.Fatal("requesting a keyframe before giving NACK repair a chance")
	}
	buffer.expire(now.Add(packetRepairAge), deliver, loss)
	if losses != 2 || len(buffer.pending) != 0 || !buffer.deadline().IsZero() || !slices.Equal(delivered[4:], []uint16{3, 5}) {
		t.Fatalf("expiry retained unresolved packets: %v, losses=%d retained=%d", delivered, losses, len(buffer.pending))
	}
	buffer.push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 2}}, now, deliver, loss)
	if len(delivered) != 6 {
		t.Fatal("late repair replayed an expired frame")
	}
}

func TestRTPReorderBoundsUntrustedSequenceJumps(t *testing.T) {
	var buffer rtpReorder
	count, losses := 0, 0
	deliver := func(receivedPacket) { count++ }
	loss := func() { losses++ }
	now := time.Now()
	buffer.push(&rtp.Packet{Header: rtp.Header{SequenceNumber: 0}}, now, deliver, loss)
	for sequence := uint16(2); sequence <= maxFramePackets; sequence++ {
		buffer.push(&rtp.Packet{Header: rtp.Header{SequenceNumber: sequence}}, now, deliver, loss)
	}
	if len(buffer.pending) > maxFramePackets {
		t.Fatal("reorder window grew past its bound")
	}
	buffer.push(&rtp.Packet{Header: rtp.Header{SequenceNumber: maxFramePackets + 2}}, now, deliver, loss)
	if len(buffer.pending) != 0 || losses != 1 || count != 2 {
		t.Fatal("large gap did not reset the bounded window")
	}
}

func BenchmarkRTPReorder(b *testing.B) {
	var buffer rtpReorder
	packet := &rtp.Packet{}
	deliver := func(receivedPacket) {}
	loss := func() {}
	b.ReportAllocs()
	for i := range b.N {
		packet.SequenceNumber = uint16(i)
		buffer.push(packet, time.Time{}, deliver, loss)
	}
}
