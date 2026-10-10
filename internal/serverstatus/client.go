// Package serverstatus queries public aggregate status without joining a server.
package serverstatus

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"net"
	"net/netip"
	"time"

	"uniclog.io/govts/internal/protocol"
	"uniclog.io/govts/internal/transport/udp"
)

// resendDelays are the gaps between repeated requests within the 2s timeout.
var resendDelays = []time.Duration{400 * time.Millisecond, 600 * time.Millisecond}

func Query(ctx context.Context, endpoint netip.AddrPort) (uint32, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return 0, err
	}
	id := binary.BigEndian.Uint32(nonce[:4])
	if id == 0 {
		id = 1
	}
	raw, err := (&net.Dialer{}).DialContext(ctx, "udp", endpoint.String())
	if err != nil {
		return 0, err
	}
	conn, err := udp.NewClientPacketConn(raw.(*net.UDPConn), protocol.NewSecureDatagramCodec(false))
	if err != nil {
		_ = raw.Close()
		return 0, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := conn.SetReadDeadline(deadline); err != nil {
		return 0, err
	}
	request := protocol.NewServerStatusRequest(id, nonce)
	if err := conn.SendPacket(request); err != nil {
		return 0, err
	}
	// A single lost datagram must not mark the server unavailable: repeat the
	// identical request; duplicate acks are ignored by the receive loop.
	resendCtx, stopResend := context.WithCancel(ctx)
	defer stopResend()
	go func() {
		for _, delay := range resendDelays {
			select {
			case <-resendCtx.Done():
				return
			case <-time.After(delay):
			}
			_ = conn.SendPacket(request)
		}
	}()
	for {
		packet, err := conn.ReceivePacket()
		if err != nil {
			return 0, err
		}
		got, count, err := protocol.DecodeServerStatus(packet)
		if err == nil && packet.Type == protocol.PacketServerStatusAck && packet.RequestID == id && got == nonce {
			return count, nil
		}
		if err := ctx.Err(); err != nil {
			return 0, err
		}
	}
}
