package media

import (
	"slices"
	"time"

	"github.com/pion/rtp"
)

type receivedPacket struct {
	packet *rtp.Packet
	at     time.Time
}

// Owned exclusively by the publisher worker. It lets Pion's NACK repairs fill
// gaps before invalidating a frame, and ignores duplicate/late retransmissions.
// The common in-order path allocates nothing; storage is created on the first
// gap and bounded to the cache window. Deadlines release unresolved gaps even
// when the source stops sending. Serial arithmetic handles uint16 wraparound.
// filled counts packets that closed a hole behind the highest received sequence
// number (NACK repair or network reordering); lost counts sequence numbers
// skipped after packetRepairAge. Discontinuities are not counted as loss.
type rtpReorder struct {
	expected    uint16
	highest     uint16
	initialized bool
	pending     map[uint16]receivedPacket
	gapAt       time.Time
	filled      uint64
	lost        uint64
}

func (r *rtpReorder) push(packet *rtp.Packet, at time.Time, deliver func(receivedPacket), loss func()) {
	if !r.initialized {
		r.expected, r.highest, r.initialized = packet.SequenceNumber, packet.SequenceNumber, true
	}
	distance := int16(packet.SequenceNumber - r.expected)
	// A late packet is ignored only near the cursor; a larger jump in either
	// direction is a sequence discontinuity and must not stall forwarding.
	if distance < 0 && distance >= -maxFramePackets {
		return
	}
	if distance < -maxFramePackets || distance > maxFramePackets {
		clear(r.pending)
		r.gapAt = time.Time{}
		r.expected, r.highest = packet.SequenceNumber, packet.SequenceNumber
		loss()
	}
	if ahead := int16(packet.SequenceNumber - r.highest); ahead > 0 {
		r.highest = packet.SequenceNumber
	} else if ahead < 0 {
		if _, exists := r.pending[packet.SequenceNumber]; !exists {
			r.filled++
		}
	}
	if packet.SequenceNumber == r.expected {
		deliver(receivedPacket{packet, at})
		r.expected++
		r.drain(deliver)
		return
	}
	if r.pending == nil {
		r.pending = make(map[uint16]receivedPacket)
	}
	if _, exists := r.pending[packet.SequenceNumber]; exists {
		return
	}
	r.pending[packet.SequenceNumber] = receivedPacket{packet, at}
	if r.gapAt.IsZero() {
		r.gapAt = at
	}
}

func (r *rtpReorder) drain(deliver func(receivedPacket)) {
	for {
		item, exists := r.pending[r.expected]
		if !exists {
			break
		}
		delete(r.pending, r.expected)
		deliver(item)
		r.expected++
	}
	if len(r.pending) == 0 {
		r.gapAt = time.Time{}
	}
}

func (r *rtpReorder) deadline() time.Time {
	if r.gapAt.IsZero() {
		return time.Time{}
	}
	return r.gapAt.Add(packetRepairAge)
}

func (r *rtpReorder) expire(now time.Time, deliver func(receivedPacket), loss func()) {
	if r.gapAt.IsZero() || now.Before(r.deadline()) {
		return
	}
	sequences := make([]uint16, 0, len(r.pending))
	for sequence := range r.pending {
		sequences = append(sequences, sequence)
	}
	base := r.expected
	slices.SortFunc(sequences, func(a, b uint16) int { return int(uint16(a-base)) - int(uint16(b-base)) })
	for _, sequence := range sequences {
		if sequence != r.expected {
			r.lost += uint64(uint16(sequence - r.expected))
			loss()
		}
		item := r.pending[sequence]
		delete(r.pending, sequence)
		deliver(item)
		r.expected = sequence + 1
	}
	r.gapAt = time.Time{}
}
