package client

import (
	"testing"
	"time"
	"uniclog.io/sonoryx/internal/audio"
)

func TestDeadlineJitterPacesNewestAnchorAndRejectsLateRepair(t *testing.T) {
	d := newDeadlineJitter()
	now := time.Unix(100, 0)
	f := func(n uint32) audio.MediaFrame {
		return audio.MediaFrame{SenderID: 8, Sequence: n, Data: []byte{1}, Duration: frameDuration}
	}
	d.Push([]audio.MediaFrame{f(1), f(2), f(3)}, now)
	if !d.due(3).Equal(now.Add(40 * time.Millisecond)) {
		t.Fatal("anchor used oldest copy")
	}
	for i := 0; i < 3; i++ {
		out := d.Tick(now.Add(time.Duration(i) * frameDuration))
		if len(out) != 1 || out[0].Sequence != uint32(i+1) {
			t.Fatal("incorrect pacing", out)
		}
	}
	d.Push([]audio.MediaFrame{f(5)}, now.Add(60*time.Millisecond))
	out := d.Tick(now.Add(60 * time.Millisecond))
	if len(out) != 1 || !out[0].Missing || out[0].Sequence != 4 {
		t.Fatal("missing PLC", out)
	}
	d.Push([]audio.MediaFrame{f(4)}, now.Add(61*time.Millisecond))
	out = d.Tick(now.Add(80 * time.Millisecond))
	if len(out) != 1 || out[0].Missing || out[0].Sequence != 5 {
		t.Fatal("late repair replayed", out)
	}
	if d.target != 60*time.Millisecond {
		t.Fatal("late arrival did not increase future reserve")
	}
	d.Push([]audio.MediaFrame{f(6)}, now.Add(time.Second))
	if d.reserve != 60*time.Millisecond || !d.due(6).Equal(now.Add(time.Second+60*time.Millisecond)) {
		t.Fatal("talkspurt did not apply reserve")
	}
}

func TestDeadlineJitterSkipsConsumerBacklogAndBoundsPLC(t *testing.T) {
	d := newDeadlineJitter()
	now := time.Unix(100, 0)
	f := func(n uint32) audio.MediaFrame { return audio.MediaFrame{Sequence: n, Data: []byte{1}} }
	d.Push([]audio.MediaFrame{f(1)}, now)
	d.Push([]audio.MediaFrame{f(10)}, now.Add(20*time.Millisecond))
	for i := 0; i < 3; i++ {
		out := d.Tick(now.Add(time.Duration(i+2) * frameDuration))
		if len(out) != 1 {
			t.Fatal("expected paced frame/PLC")
		}
	}
	// A delayed consumer never receives a burst to catch up.
	if out := d.Tick(now.Add(220 * time.Millisecond)); len(out) > 1 {
		t.Fatal("catch-up batch")
	}
	if out := d.Tick(now.Add(time.Second)); len(out) != 0 {
		t.Fatal("PLC continues after tail")
	}
}

func TestDeadlineJitterResumesAfterShortCapturePause(t *testing.T) {
	d := newDeadlineJitter()
	now := time.Unix(100, 0)
	f := func(n uint32) audio.MediaFrame { return audio.MediaFrame{Sequence: n, Data: []byte{1}} }
	d.Push([]audio.MediaFrame{f(1)}, now)
	if out := d.Tick(now.Add(40 * time.Millisecond)); len(out) != 1 {
		t.Fatal("initial frame was not played")
	}
	// VAD suppresses capture without allocating voice frame sequences. A
	// short pause must not leave all subsequent frames behind the old clock.
	d.Push([]audio.MediaFrame{f(2)}, now.Add(120*time.Millisecond))
	if out := d.Tick(now.Add(160 * time.Millisecond)); len(out) != 1 || out[0].Sequence != 2 || out[0].Missing {
		t.Fatal("speech did not resume after the short pause", out)
	}
	anchor := d.anchor
	// An older repair arriving after a long silence cannot restart playout.
	d.Push([]audio.MediaFrame{f(1)}, now.Add(400*time.Millisecond))
	if !d.anchor.Equal(anchor) || len(d.pending) != 0 {
		t.Fatal("old repair restarted the playout clock")
	}
}
