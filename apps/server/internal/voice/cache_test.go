package voice

import (
	"bytes"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/protocol"
)

func TestRequestCacheExpiresEntries(t *testing.T) {
	now := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	cache := newRequestCache(10*time.Second, 10, func() time.Time {
		return now
	})

	cache.Put(1, 7, protocol.NewErrorPacket(1, 7, "error"))
	if _, ok := cache.Get(1, 7); !ok {
		t.Fatal("Get() did not return a live entry")
	}

	now = now.Add(10 * time.Second)
	if _, ok := cache.Get(1, 7); ok {
		t.Fatal("Get() returned an expired entry")
	}
	if got := cache.Len(); got != 0 {
		t.Fatalf("Len() = %d, want 0", got)
	}
}

func TestRequestCacheEvictsOldestEntryAtCapacity(t *testing.T) {
	now := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	cache := newRequestCache(time.Minute, 2, func() time.Time {
		return now
	})

	cache.Put(1, 1, protocol.NewErrorPacket(1, 1, "first"))
	now = now.Add(time.Second)
	cache.Put(1, 2, protocol.NewErrorPacket(1, 2, "second"))
	now = now.Add(time.Second)
	cache.Put(1, 3, protocol.NewErrorPacket(1, 3, "third"))

	if _, ok := cache.Get(1, 1); ok {
		t.Fatal("Get() returned the oldest entry after capacity eviction")
	}
	if _, ok := cache.Get(1, 2); !ok {
		t.Fatal("Get() did not return the second entry")
	}
	if _, ok := cache.Get(1, 3); !ok {
		t.Fatal("Get() did not return the newest entry")
	}
}

func TestRequestCacheRemovesSession(t *testing.T) {
	now := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	cache := newRequestCache(time.Minute, 10, func() time.Time {
		return now
	})

	cache.Put(1, 1, protocol.NewErrorPacket(1, 1, "first"))
	cache.Put(1, 2, protocol.NewErrorPacket(1, 2, "second"))
	cache.Put(2, 1, protocol.NewErrorPacket(2, 1, "other session"))

	if removed := cache.RemoveSession(1); removed != 2 {
		t.Fatalf("RemoveSession() = %d, want 2", removed)
	}
	if _, ok := cache.Get(1, 1); ok {
		t.Fatal("Get() returned an entry for the removed session")
	}
	if _, ok := cache.Get(2, 1); !ok {
		t.Fatal("RemoveSession() removed another session")
	}
}

func TestRequestCacheCopiesPayload(t *testing.T) {
	now := time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)
	cache := newRequestCache(time.Minute, 10, func() time.Time {
		return now
	})
	payload := []byte("music")

	cache.Put(1, 1, protocol.VoicePacket{Payload: payload})
	payload[0] = 'x'

	packet, ok := cache.Get(1, 1)
	if !ok {
		t.Fatal("Get() did not return a cached packet")
	}
	if !bytes.Equal(packet.Payload, []byte("music")) {
		t.Fatalf("Get() payload = %q, want %q", packet.Payload, "music")
	}

	packet.Payload[0] = 'y'
	packetAgain, ok := cache.Get(1, 1)
	if !ok {
		t.Fatal("second Get() did not return a cached packet")
	}
	if !bytes.Equal(packetAgain.Payload, []byte("music")) {
		t.Fatalf("second Get() payload = %q, want %q", packetAgain.Payload, "music")
	}
}
