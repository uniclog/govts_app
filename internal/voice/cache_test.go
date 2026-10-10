package voice

import (
	"bytes"
	"net/netip"
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

func TestRequestCacheRemovesHandshakeEndpoint(t *testing.T) {
	cache := NewRequestCache()
	endpoint := netip.MustParseAddrPort("127.0.0.1:9000")
	other := netip.MustParseAddrPort("127.0.0.1:9001")
	cache.PutHandshake(endpoint, 1, protocol.VoicePacket{Type: protocol.PacketHelloAck})
	cache.PutHandshake(other, 1, protocol.VoicePacket{Type: protocol.PacketHelloAck})
	cache.Put(7, 1, protocol.VoicePacket{Type: protocol.PacketJoinChannelAck})
	if removed := cache.RemoveHandshakeEndpoint(endpoint); removed != 1 {
		t.Fatalf("removed = %d", removed)
	}
	if _, ok := cache.GetHandshake(endpoint, 1); ok {
		t.Fatal("endpoint handshake remains")
	}
	if _, ok := cache.GetHandshake(other, 1); !ok {
		t.Fatal("other handshake removed")
	}
	if _, ok := cache.Get(7, 1); !ok {
		t.Fatal("session response removed")
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

func TestRequestCacheSeparatesHandshakeEndpoints(t *testing.T) {
	now := time.Date(2026, time.September, 9, 12, 0, 0, 0, time.UTC)
	cache := newRequestCache(time.Minute, 10, func() time.Time {
		return now
	})
	firstEndpoint := netip.MustParseAddrPort("127.0.0.1:5001")
	secondEndpoint := netip.MustParseAddrPort("127.0.0.1:5002")

	cache.PutHandshake(firstEndpoint, 7, protocol.VoicePacket{
		Type:      protocol.PacketHelloAck,
		SessionID: 1,
		RequestID: 7,
	})
	cache.PutHandshake(secondEndpoint, 7, protocol.VoicePacket{
		Type:      protocol.PacketHelloAck,
		SessionID: 2,
		RequestID: 7,
	})

	first, ok := cache.GetHandshake(firstEndpoint, 7)
	if !ok || first.SessionID != 1 {
		t.Fatalf("first endpoint response = %+v, found = %t", first, ok)
	}
	second, ok := cache.GetHandshake(secondEndpoint, 7)
	if !ok || second.SessionID != 2 {
		t.Fatalf("second endpoint response = %+v, found = %t", second, ok)
	}
}
