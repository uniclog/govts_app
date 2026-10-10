package voice

import (
	"bytes"
	"net/netip"
	"sync"
	"time"

	"uniclog.io/sonoryx/internal/protocol"
)

const (
	RequestCacheTTL        = 30 * time.Second
	RequestCacheMaxEntries = 4096
)

type requestKey struct {
	SessionID uint64
	RequestID uint32
	Endpoint  netip.AddrPort
}

type requestCacheEntry struct {
	response  protocol.VoicePacket
	expiresAt time.Time
}

type RequestCache struct {
	mu sync.Mutex

	responses  map[requestKey]requestCacheEntry
	ttl        time.Duration
	maxEntries int
	now        func() time.Time
}

func NewRequestCache() *RequestCache {
	return newRequestCache(
		RequestCacheTTL,
		RequestCacheMaxEntries,
		time.Now,
	)
}

func newRequestCache(
	ttl time.Duration,
	maxEntries int,
	now func() time.Time,
) *RequestCache {
	if ttl <= 0 {
		panic("request cache TTL must be positive")
	}
	if maxEntries <= 0 {
		panic("request cache max entries must be positive")
	}
	if now == nil {
		panic("request cache clock is required")
	}

	return &RequestCache{
		responses:  make(map[requestKey]requestCacheEntry),
		ttl:        ttl,
		maxEntries: maxEntries,
		now:        now,
	}
}

func (c *RequestCache) Get(
	sessionID uint64,
	requestID uint32,
) (protocol.VoicePacket, bool) {
	return c.get(requestKey{
		SessionID: sessionID,
		RequestID: requestID,
	})
}

func (c *RequestCache) GetHandshake(
	endpoint netip.AddrPort,
	requestID uint32,
) (protocol.VoicePacket, bool) {
	return c.get(requestKey{
		RequestID: requestID,
		Endpoint:  endpoint,
	})
}

func (c *RequestCache) get(key requestKey) (protocol.VoicePacket, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.responses[key]
	if !ok {
		return protocol.VoicePacket{}, false
	}
	if !c.now().Before(entry.expiresAt) {
		delete(c.responses, key)
		return protocol.VoicePacket{}, false
	}

	return clonePacket(entry.response), true
}

func (c *RequestCache) Put(
	sessionID uint64,
	requestID uint32,
	response protocol.VoicePacket,
) {
	c.put(
		requestKey{
			SessionID: sessionID,
			RequestID: requestID,
		},
		response,
	)
}

func (c *RequestCache) PutHandshake(
	endpoint netip.AddrPort,
	requestID uint32,
	response protocol.VoicePacket,
) {
	c.put(
		requestKey{
			RequestID: requestID,
			Endpoint:  endpoint,
		},
		response,
	)
}

func (c *RequestCache) put(
	key requestKey,
	response protocol.VoicePacket,
) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()
	c.removeExpiredLocked(now)

	if _, exists := c.responses[key]; !exists && len(c.responses) >= c.maxEntries {
		c.removeOldestLocked()
	}

	c.responses[key] = requestCacheEntry{
		response:  clonePacket(response),
		expiresAt: now.Add(c.ttl),
	}
}

func (c *RequestCache) RemoveSession(sessionID uint64) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	removed := 0
	for key := range c.responses {
		if key.SessionID == sessionID {
			delete(c.responses, key)
			removed++
		}
	}
	return removed
}

func (c *RequestCache) RemoveHandshakeEndpoint(endpoint netip.AddrPort) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	removed := 0
	for key := range c.responses {
		if key.SessionID == 0 && key.Endpoint == endpoint {
			delete(c.responses, key)
			removed++
		}
	}
	return removed
}

func (c *RequestCache) RemoveExpired() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.removeExpiredLocked(c.now())
}

func (c *RequestCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.removeExpiredLocked(c.now())
	return len(c.responses)
}

func (c *RequestCache) removeExpiredLocked(now time.Time) int {
	removed := 0
	for key, entry := range c.responses {
		if !now.Before(entry.expiresAt) {
			delete(c.responses, key)
			removed++
		}
	}
	return removed
}

func (c *RequestCache) removeOldestLocked() {
	var oldestKey requestKey
	var oldestExpiry time.Time
	found := false

	for key, entry := range c.responses {
		if !found || entry.expiresAt.Before(oldestExpiry) {
			oldestKey = key
			oldestExpiry = entry.expiresAt
			found = true
		}
	}
	if found {
		delete(c.responses, oldestKey)
	}
}

func clonePacket(packet protocol.VoicePacket) protocol.VoicePacket {
	packet.Payload = bytes.Clone(packet.Payload)
	return packet
}
