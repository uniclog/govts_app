package protocol

import "testing"

func TestAudioStatePayloadRoundTrip(t *testing.T) {
	for _, want := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
		payload := EncodeAudioState(want[0], want[1])
		muted, deafened, err := DecodeAudioState(payload)
		if err != nil || muted != want[0] || deafened != want[1] {
			t.Fatalf("round trip = (%t, %t, %v), want %v", muted, deafened, err, want)
		}
	}
	for _, payload := range [][]byte{nil, {}, {0, 0}, {4}} {
		if _, _, err := DecodeAudioState(payload); err == nil {
			t.Fatalf("accepted malformed audio state %x", payload)
		}
	}
}
