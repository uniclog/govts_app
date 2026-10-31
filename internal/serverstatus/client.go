// Package serverstatus queries public aggregate status without joining a server.
package serverstatus

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"net"
	"net/netip"
	"time"

	"uniclog.io/sonoryx/internal/appversion"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

// resendDelays are the gaps between repeated requests within the 2s timeout.
var resendDelays = []time.Duration{400 * time.Millisecond, 600 * time.Millisecond}

func Query(ctx context.Context, endpoint netip.AddrPort) (uint32, error) {
	count, _, err := query(ctx, endpoint, false)
	return count, err
}

// QueryDetails tries the extended public status first, with a legacy fallback
// after one second. Both attempts share the existing two-second total deadline.
func QueryDetails(ctx context.Context, endpoint netip.AddrPort) (uint32, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	extended, cancelExtended := context.WithTimeout(ctx, time.Second)
	count, version, err := query(extended, endpoint, true)
	cancelExtended()
	if err == nil {
		return count, version, nil
	}
	if ctx.Err() != nil {
		return 0, "", ctx.Err()
	}
	return query(ctx, endpoint, false)
}

func query(ctx context.Context, endpoint netip.AddrPort, extended bool) (uint32, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return 0, "", err
	}
	id := binary.BigEndian.Uint32(nonce[:4])
	if id == 0 {
		id = 1
	}
	raw, err := (&net.Dialer{}).DialContext(ctx, "udp", endpoint.String())
	if err != nil {
		return 0, "", err
	}
	conn, err := udp.NewClientPacketConn(raw.(*net.UDPConn), protocol.NewSecureDatagramCodec(false))
	if err != nil {
		_ = raw.Close()
		return 0, "", err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := ctx.Deadline()
	if err := conn.SetReadDeadline(deadline); err != nil {
		return 0, "", err
	}
	request := protocol.NewServerStatusRequest(id, nonce)
	if extended {
		request = protocol.NewServerStatusRequestV2(id, nonce)
	}
	if err := conn.SendPacket(request); err != nil {
		return 0, "", err
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
			return 0, "", err
		}
		got, count, err := protocol.DecodeServerStatus(packet)
		if err == nil && packet.Type == protocol.PacketServerStatusAck && packet.RequestID == id && got == nonce {
			version := ""
			if number, ok := protocol.ServerStatusAckVersion(packet); ok {
				version = appversion.Number(number).String()
			}
			return count, version, nil
		}
		if err := ctx.Err(); err != nil {
			return 0, "", err
		}
	}
}
