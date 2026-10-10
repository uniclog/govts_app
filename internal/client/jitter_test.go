package client

import (
	"math"
	"reflect"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/audio"
)

func TestJitterBufferReordersFrames(t *testing.T) {
	buffer := newJitterBuffer(3)

	if got := buffer.Push(mediaFrame(2)); len(got) != 0 {
		t.Fatalf("Push(2) released sequences %v, want none", sequences(got))
	}
	if got := buffer.Push(mediaFrame(1)); len(got) != 0 {
		t.Fatalf("Push(1) released sequences %v, want none", sequences(got))
	}
	if got := sequences(buffer.Push(mediaFrame(3))); !reflect.DeepEqual(got, []uint32{1, 2, 3}) {
		t.Fatalf("Push(3) released sequences %v, want [1 2 3]", got)
	}
}

func TestJitterBufferSkipsLostFrameAfterWindowFills(t *testing.T) {
	buffer := newJitterBuffer(2)

	buffer.Push(mediaFrame(1))
	buffer.Push(mediaFrame(2))
	if got := buffer.Push(mediaFrame(4)); len(got) != 0 {
		t.Fatalf("Push(4) released sequences %v, want none", sequences(got))
	}
	released := buffer.Push(mediaFrame(5))
	if got := sequences(released); !reflect.DeepEqual(got, []uint32{3, 4, 5}) {
		t.Fatalf("Push(5) released sequences %v, want [3 4 5]", got)
	}
	if !released[0].Missing || released[1].Missing || released[2].Missing {
		t.Fatalf("missing flags = %v %v %v, want only the lost frame marked", released[0].Missing, released[1].Missing, released[2].Missing)
	}
	if stats := buffer.Stats(); stats.Lost != 1 || stats.Concealed != 1 {
		t.Fatalf("lost/concealed frames = %d/%d, want 1/1", stats.Lost, stats.Concealed)
	}
}

func TestJitterBufferReleasesShortStreamAfterDelay(t *testing.T) {
	buffer := newJitterBuffer(3)

	buffer.Push(mediaFrame(7))
	if got := buffer.Tick(); len(got) != 0 {
		t.Fatalf("first Tick() released sequences %v, want none", sequences(got))
	}
	if got := buffer.Tick(); len(got) != 0 {
		t.Fatalf("second Tick() released sequences %v, want none", sequences(got))
	}
	if got := sequences(buffer.Tick()); !reflect.DeepEqual(got, []uint32{7}) {
		t.Fatalf("third Tick() released sequences %v, want [7]", got)
	}
}

func TestJitterBufferDropsDuplicatesAndTooOldFrames(t *testing.T) {
	buffer := newJitterBuffer(3)

	buffer.Push(mediaFrame(2))
	buffer.Push(mediaFrame(2))
	buffer.Push(mediaFrame(1))
	buffer.Push(mediaFrame(3))
	buffer.Push(mediaFrame(2))

	stats := buffer.Stats()
	if stats.Duplicates != 1 {
		t.Fatalf("duplicate frames = %d, want 1", stats.Duplicates)
	}
	if stats.TooOld != 1 {
		t.Fatalf("too-old frames = %d, want 1", stats.TooOld)
	}
}

func TestJitterBufferHandlesSequenceWraparound(t *testing.T) {
	buffer := newJitterBuffer(2)

	buffer.Push(mediaFrame(math.MaxUint32))
	got := sequences(buffer.Push(mediaFrame(0)))
	if !reflect.DeepEqual(got, []uint32{math.MaxUint32, 0}) {
		t.Fatalf("released sequences %v, want [%d 0]", got, uint32(math.MaxUint32))
	}
}

func TestStreamJitterBuffersKeepSendersIndependent(t *testing.T) {
	buffers := newStreamJitterBuffers(2)

	buffers.Push(senderFrame(10, 2))
	buffers.Push(senderFrame(20, 10))
	first := buffers.Push(senderFrame(10, 1))
	second := buffers.Push(senderFrame(20, 11))

	if got := sequences(first); !reflect.DeepEqual(got, []uint32{1, 2}) {
		t.Fatalf("sender 10 sequences = %v, want [1 2]", got)
	}
	if got := sequences(second); !reflect.DeepEqual(got, []uint32{10, 11}) {
		t.Fatalf("sender 20 sequences = %v, want [10 11]", got)
	}
}

func TestStreamJitterBuffersRemoveInactive(t *testing.T) {
	now := time.Now()
	buffers := newStreamJitterBuffers(3)
	buffers.PushAt(senderFrame(10, 1), now.Add(-DefaultStreamIdleTimeout))
	buffers.PushAt(senderFrame(20, 1), now.Add(-time.Second))

	if removed := buffers.RemoveInactive(now, DefaultStreamIdleTimeout); removed != 1 {
		t.Fatalf("RemoveInactive() = %d, want 1", removed)
	}
	if _, ok := buffers.bySender[10]; ok {
		t.Fatal("inactive jitter buffer was not removed")
	}
	if _, ok := buffers.bySender[20]; !ok {
		t.Fatal("active jitter buffer was removed")
	}
}

func mediaFrame(sequence uint32) audio.MediaFrame {
	return senderFrame(1, sequence)
}

func senderFrame(senderID uint64, sequence uint32) audio.MediaFrame {
	return audio.MediaFrame{SenderID: senderID, Sequence: sequence}
}

func sequences(frames []audio.MediaFrame) []uint32 {
	result := make([]uint32, len(frames))
	for i, frame := range frames {
		result[i] = frame.Sequence
	}
	return result
}
