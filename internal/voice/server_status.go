package voice

import (
	"net"
	"net/netip"
	"time"

	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func (h *Hub) SetPublicStatus(enabled bool) {
	h.mu.Lock()
	h.publicStatus = enabled
	h.mu.Unlock()
}

type statusRateWindow struct {
	start time.Time
	count int
}
type statusLimiter struct {
	ips    map[netip.Addr]statusRateWindow
	global statusRateWindow
	pruned time.Time
}

// Owned by the single ServeUDP loop. Bounded memory and no session RequestCache.
func (l *statusLimiter) allow(ip netip.Addr, now time.Time) bool {
	if now.Sub(l.global.start) >= time.Second {
		l.global = statusRateWindow{start: now}
	}
	if l.global.count >= 100 {
		return false
	}
	if l.ips == nil {
		l.ips = make(map[netip.Addr]statusRateWindow)
	}
	if now.Sub(l.pruned) >= time.Minute {
		for address, window := range l.ips {
			if now.Sub(window.start) >= time.Minute {
				delete(l.ips, address)
			}
		}
		l.pruned = now
	}
	ip = ip.Unmap()
	window, exists := l.ips[ip]
	if !exists && len(l.ips) >= 4096 {
		return false
	}
	if now.Sub(window.start) >= time.Minute {
		window = statusRateWindow{start: now}
	}
	if window.count >= 120 {
		return false
	}
	window.count++
	l.global.count++
	l.ips[ip] = window
	return true
}

func handleServerStatus(conn *udp.ServerPacketConn, hub *Hub, limiter *statusLimiter, packet protocol.VoicePacket, addr *net.UDPAddr) {
	nonce, _, err := protocol.DecodeServerStatus(packet)
	if err != nil || addr == nil || packet.Type != protocol.PacketServerStatusRequest {
		return
	}
	hub.mu.RLock()
	enabled, count := hub.publicStatus, len(hub.sessions)
	hub.mu.RUnlock()
	if !enabled || !limiter.allow(addr.AddrPort().Addr(), time.Now()) {
		return
	}
	// No error response: the 38-byte reply must never exceed its 49-byte request.
	_ = conn.WritePacket(0, addr, protocol.NewServerStatusAck(packet.RequestID, nonce, uint32(count)))
}
