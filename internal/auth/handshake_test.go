package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

func TestInitWatchFlagRoundTripsAndIsSigned(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	init, _, err := NewInit(strings.Repeat("n", 64), private)
	if err != nil {
		t.Fatal(err)
	}
	for _, watch := range []bool{false, true} {
		init.Watch = watch
		payload := init.Encode()
		if got := payload[0]&initWatchFlag != 0; got != watch {
			t.Fatalf("watch=%t: flag bit = %t", watch, got)
		}
		decoded, err := DecodeInit(payload)
		if err != nil {
			t.Fatalf("watch=%t: %v", watch, err)
		}
		if decoded != init {
			t.Fatalf("DecodeInit() = %+v, want %+v", decoded, init)
		}
	}
	// The flag is part of AuthInit, so it is part of the signed transcript.
	_, signer, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	init.Watch = false
	challenge, _, err := NewChallenge(init, signer, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	watcher := init
	watcher.Watch = true
	if Transcript(init, challenge, 1, 1) == Transcript(watcher, challenge, 1, 1) {
		t.Fatal("watch flag does not change the transcript")
	}
	if err := VerifyChallenge(watcher, challenge, 1, 1); err == nil {
		t.Fatal("a challenge for a participant Init verified for a watcher Init")
	}
}

func TestDecodeInitRejectsBadNameLength(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	init, _, err := NewInit("alice", private)
	if err != nil {
		t.Fatal(err)
	}
	payload := init.Encode()
	for name, mutate := range map[string]func([]byte) []byte{
		"zero name":       func(b []byte) []byte { b[0] = 0; return b[:InitFixedSize] },
		"watch zero name": func(b []byte) []byte { b[0] = initWatchFlag; return b[:InitFixedSize] },
		"length mismatch": func(b []byte) []byte { b[0]++; return b },
		"name over 64":    func(b []byte) []byte { b[0] = 65; return append(b[:InitFixedSize], make([]byte, 65)...) },
		"watch name over 64": func(b []byte) []byte {
			b[0] = initWatchFlag | 65
			return append(b[:InitFixedSize], make([]byte, 65)...)
		},
		"short": func(b []byte) []byte { return b[:InitFixedSize-1] },
	} {
		if _, err := DecodeInit(mutate(append([]byte(nil), payload...))); err == nil {
			t.Errorf("%s: DecodeInit accepted", name)
		}
	}
}
