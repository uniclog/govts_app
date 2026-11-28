package clientcore

import (
	"errors"
	"math/rand/v2"
	"net"
	"time"
)

func shouldReplaceClientSocket(err error) bool {
	return errors.Is(err, net.ErrClosed)
}

type reconnectBackoff struct{ next time.Duration }

func newReconnectBackoff() *reconnectBackoff { return &reconnectBackoff{next: time.Second} }
func (backoff *reconnectBackoff) Next() time.Duration {
	current := backoff.next
	if backoff.next < 4*time.Second {
		backoff.next *= 2
		if backoff.next > 4*time.Second {
			backoff.next = 4 * time.Second
		}
	}
	return current
}
func (backoff *reconnectBackoff) Reset() { backoff.next = time.Second }

// handshakesBeforeSocketReplacement limits how long a connected UDP socket can
// keep failing: after a network change it may stay on the old route. A new
// port leaves any old session on the server until its timeout, so a short
// failure keeps the socket.
const handshakesBeforeSocketReplacement = 3

// reconnectDelay spreads each delay by ±20 %, so clients dropped together by a
// server restart do not return in the same instant.
func reconnectDelay(delay time.Duration) time.Duration {
	spread := 0.8 + 0.4*rand.Float64()
	return time.Duration(float64(delay) * spread).Round(100 * time.Millisecond)
}
