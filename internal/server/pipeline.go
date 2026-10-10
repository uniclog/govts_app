package server

import (
	"context"
	"log"
	"sync"
	"time"

	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/voice"
)

const (
	SessionTimeout  = 30 * time.Second
	CleanupInterval = 5 * time.Second
)

func CleanupLoop(
	ctx context.Context,
	hub *voice.Hub,
	cache *voice.RequestCache,
	timeout time.Duration,
	interval time.Duration,
	codecs ...*protocol.SecureDatagramCodec,
) error {
	return cleanupLoop(ctx, hub, cache, timeout, interval, nil, codecs...)
}

// CleanupLoopWithPolicyGate keeps timeout removals from racing with durable
// moderation audits and console privilege changes.
func CleanupLoopWithPolicyGate(
	ctx context.Context,
	hub *voice.Hub,
	cache *voice.RequestCache,
	timeout time.Duration,
	interval time.Duration,
	gate *sync.Mutex,
	codecs ...*protocol.SecureDatagramCodec,
) error {
	return cleanupLoop(ctx, hub, cache, timeout, interval, gate, codecs...)
}

func cleanupLoop(ctx context.Context, hub *voice.Hub, cache *voice.RequestCache, timeout, interval time.Duration, gate *sync.Mutex, codecs ...*protocol.SecureDatagramCodec) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case now := <-ticker.C:
			if gate != nil {
				gate.Lock()
			}
			removed := hub.RemoveInactive(now, timeout)

			for _, session := range removed {
				if len(codecs) > 0 && codecs[0] != nil {
					codecs[0].Remove(session.ID)
				}
				cache.RemoveSession(session.ID)
				log.Printf(
					"session timed out: id=%d name=%q addr=%v last_seen=%s idle=%s timeout=%s",
					session.ID,
					session.Name,
					session.Addr,
					session.LastSeen.Local().Format(time.RFC3339Nano),
					now.Sub(session.LastSeen),
					timeout,
				)
			}
			cache.RemoveExpired()
			if len(codecs) > 0 && codecs[0] != nil {
				for _, id := range codecs[0].SessionIDs() {
					if _, ok := hub.Get(id); !ok {
						codecs[0].Remove(id)
						cache.RemoveSession(id)
					}
				}
			}
			if gate != nil {
				gate.Unlock()
			}
		}
	}
}
