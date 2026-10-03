package voice

import (
	"encoding/binary"
	"net"
	"net/netip"
	"sync"
	"time"

	"uniclog.io/govts/internal/protocol"
	"uniclog.io/govts/internal/transport/udp"
)

const (
	serverStatusWindow = 10 * time.Second
	serverStatusLimit  = 8
)

type serverStatusWindowState struct {
	started time.Time
	count   int
}

var serverStatusLimiter = struct {
	sync.Mutex
	windows map[netip.Addr]serverStatusWindowState
}{windows: map[netip.Addr]serverStatusWindowState{}}

func allowServerStatus(addr *net.UDPAddr) bool {
	if addr == nil {
		return false
	}
	ip := addr.AddrPort().Addr().Unmap()
	if !ip.IsValid() {
		return false
	}
	now := time.Now()
	serverStatusLimiter.Lock()
	defer serverStatusLimiter.Unlock()
	if len(serverStatusLimiter.windows) > 4096 {
		for key, window := range serverStatusLimiter.windows {
			if now.Sub(window.started) >= serverStatusWindow {
				delete(serverStatusLimiter.windows, key)
			}
		}
	}
	window := serverStatusLimiter.windows[ip]
	if now.Sub(window.started) >= serverStatusWindow {
		window = serverStatusWindowState{started: now}
	}
	if window.count >= serverStatusLimit {
		serverStatusLimiter.windows[ip] = window
		return false
	}
	window.count++
	serverStatusLimiter.windows[ip] = window
	return true
}

func HandleServerStatusPacket(conn *udp.ServerPacketConn, hub *Hub, packet protocol.VoicePacket, addr *net.UDPAddr) error {
	if packet.SessionID != 0 || len(packet.Payload) != 0 || !allowServerStatus(addr) {
		return nil
	}
	var payload [4]byte
	binary.BigEndian.PutUint32(payload[:], uint32(hub.Count()))
	return conn.WritePacket(0, addr, protocol.VoicePacket{
		Type:      protocol.PacketServerStatusAck,
		RequestID: packet.RequestID,
		Payload:   payload[:],
	})
}
