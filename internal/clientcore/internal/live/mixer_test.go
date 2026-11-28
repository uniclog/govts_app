package client

import (
	"reflect"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/audio"
)

func TestMixPCMFramesSumsAndClampsSamples(t *testing.T) {
	frames := []audio.MediaPCMFrame{
		{Samples: []int16{30000, -30000, 100}, Duration: 10 * time.Millisecond},
		{Samples: []int16{10000, -10000, -50}, Duration: 20 * time.Millisecond},
	}

	got := mixPCMFrames(frames)
	want := audio.PCMFrame{
		Samples:  []int16{32767, -32768, 50},
		Duration: 20 * time.Millisecond,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mixPCMFrames() = %+v, want %+v", got, want)
	}
}

func TestPCMMixerConsumesOneFramePerSender(t *testing.T) {
	mixer := newPCMMixer()
	mixer.Push(audio.MediaPCMFrame{SenderID: 10, Samples: []int16{100, 200}})
	mixer.Push(audio.MediaPCMFrame{SenderID: 10, Samples: []int16{300, 400}})
	mixer.Push(audio.MediaPCMFrame{SenderID: 20, Samples: []int16{10, -50}})

	first, ok := mixer.Mix()
	if !ok {
		t.Fatal("first Mix() returned no frame")
	}
	if want := []int16{110, 150}; !reflect.DeepEqual(first.Samples, want) {
		t.Fatalf("first Mix() samples = %v, want %v", first.Samples, want)
	}

	second, ok := mixer.Mix()
	if !ok {
		t.Fatal("second Mix() returned no frame")
	}
	if want := []int16{300, 400}; !reflect.DeepEqual(second.Samples, want) {
		t.Fatalf("second Mix() samples = %v, want %v", second.Samples, want)
	}

	if _, ok := mixer.Mix(); ok {
		t.Fatal("third Mix() returned a frame from empty mixer")
	}
}

func TestPCMMixerOverloadKeepsRecentAudioAndOtherSenders(t *testing.T) {
	mixer := newPCMMixer()
	mixer.Push(audio.MediaPCMFrame{SenderID: 2, Samples: []int16{1000}})
	for i := 1; i <= 100; i++ {
		mixer.Push(audio.MediaPCMFrame{SenderID: 1, Samples: []int16{int16(i)}})
	}
	if mixer.droppedFrames != 95 {
		t.Fatalf("dropped = %d, want 95", mixer.droppedFrames)
	}
	for i := 96; i <= 100; i++ {
		frame, ok := mixer.Mix()
		want := int16(i)
		if i == 96 {
			want += 1000
		}
		if !ok || !reflect.DeepEqual(frame.Samples, []int16{want}) {
			t.Fatalf("mix = %+v, want sample %d", frame, want)
		}
	}
	if _, ok := mixer.Mix(); ok {
		t.Fatal("stale audio remains after recent frames were consumed")
	}
	if len(mixer.bySender) != 0 {
		t.Fatal("empty sender queues retained")
	}
}
