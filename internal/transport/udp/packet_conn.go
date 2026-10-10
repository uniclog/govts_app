package udp

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"uniclog.io/sonoryx/internal/protocol"
)

type packetConn struct {
	conn  *net.UDPConn
	codec protocol.DatagramCodec

	readMu  sync.Mutex
	writeMu sync.Mutex
	codecMu sync.Mutex

	closeOnce sync.Once
	closeErr  error
}

// ClientPacketConn owns a connected UDP socket and exposes only client-style
// packet operations. Ownership of conn transfers when the constructor succeeds.
type ClientPacketConn struct {
	packetConn *packetConn
	remote     netip.AddrPort
	sessionID  atomic.Uint64
}

// ServerPacketConn owns an unconnected UDP socket and exposes only server-style
// packet operations. Ownership of conn transfers when the constructor succeeds.
type ServerPacketConn struct {
	packetConn *packetConn
}

// NewClientPacketConn validates the connected socket and takes ownership of it
// only when construction succeeds. On error, the caller remains responsible
// for closing conn.
func NewClientPacketConn(
	conn *net.UDPConn,
	codec protocol.DatagramCodec,
) (*ClientPacketConn, error) {
	base, err := newPacketConn(conn, codec)
	if err != nil {
		return nil, err
	}
	remoteAddr := conn.RemoteAddr()
	if remoteAddr == nil {
		return nil, errors.New("client packet connection requires a connected UDP socket")
	}
	remote, err := addrPort(remoteAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve remote UDP address: %w", err)
	}
	return &ClientPacketConn{packetConn: base, remote: remote}, nil
}

// NewServerPacketConn validates the unconnected socket and takes ownership of
// it only when construction succeeds. On error, the caller remains responsible
// for closing conn.
func NewServerPacketConn(
	conn *net.UDPConn,
	codec protocol.DatagramCodec,
) (*ServerPacketConn, error) {
	base, err := newPacketConn(conn, codec)
	if err != nil {
		return nil, err
	}
	if conn.RemoteAddr() != nil {
		return nil, errors.New("server packet connection requires an unconnected UDP socket")
	}
	return &ServerPacketConn{packetConn: base}, nil
}

func newPacketConn(
	conn *net.UDPConn,
	codec protocol.DatagramCodec,
) (*packetConn, error) {
	if conn == nil {
		return nil, errors.New("UDP connection is required")
	}
	if codec == nil {
		return nil, errors.New("datagram codec is required")
	}
	return &packetConn{conn: conn, codec: codec}, nil
}

// BindSession sets the local client session used to select outbound and
// inbound protection contexts. Binding is idempotent only for the same ID.
func (c *ClientPacketConn) BindSession(sessionID uint64) error {
	if sessionID == 0 {
		return errors.New("client session ID must not be zero")
	}
	for {
		current := c.sessionID.Load()
		if current == sessionID {
			return nil
		}
		if current != 0 {
			return fmt.Errorf(
				"client packet connection already bound to session %d",
				current,
			)
		}
		if c.sessionID.CompareAndSwap(0, sessionID) {
			return nil
		}
	}
}

// ClearSession removes exactly the expected binding. The caller must stop all
// session read/write loops before calling it.
func (c *ClientPacketConn) ClearSession(expectedSessionID uint64) error {
	if expectedSessionID == 0 {
		return errors.New("expected client session ID must not be zero")
	}
	if c.sessionID.CompareAndSwap(expectedSessionID, 0) {
		return nil
	}
	current := c.sessionID.Load()
	return fmt.Errorf(
		"client packet connection bound to session %d, expected %d",
		current,
		expectedSessionID,
	)
}

func (c *ClientPacketConn) SendPacket(packet protocol.VoicePacket) error {
	ownerID := c.sessionID.Load()
	if ownerID == 0 && packet.SessionID != 0 {
		return errors.New("client packet connection is not bound to a session")
	}
	if ownerID != 0 && packet.SessionID != ownerID {
		return fmt.Errorf(
			"packet session ID %d does not match bound session %d",
			packet.SessionID,
			ownerID,
		)
	}

	ctx := protocol.DatagramContext{
		Direction:  protocol.DirectionClientToServer,
		KeyOwnerID: ownerID,
		Endpoint:   c.remote,
	}
	return c.packetConn.write(ctx, packet, func(data []byte) error {
		_, err := c.packetConn.conn.Write(data)
		return err
	})
}

func (c *ClientPacketConn) ReceivePacket() (protocol.VoicePacket, error) {
	ctx := protocol.DatagramContext{
		Direction:  protocol.DirectionServerToClient,
		KeyOwnerID: c.sessionID.Load(),
		Endpoint:   c.remote,
	}
	return c.packetConn.read(ctx, func(buffer []byte) (int, error) {
		return c.packetConn.conn.Read(buffer)
	})
}

func (c *ClientPacketConn) SetReadDeadline(deadline time.Time) error {
	return c.packetConn.conn.SetReadDeadline(deadline)
}

func (c *ClientPacketConn) Close() error {
	return c.packetConn.close()
}

func (c *ClientPacketConn) LocalAddr() net.Addr {
	return c.packetConn.conn.LocalAddr()
}

func (c *ClientPacketConn) RemoteAddr() net.Addr {
	return c.packetConn.conn.RemoteAddr()
}

func (c *ServerPacketConn) ReadPacket() (
	protocol.VoicePacket,
	*net.UDPAddr,
	error,
) {
	c.packetConn.readMu.Lock()
	defer c.packetConn.readMu.Unlock()

	buffer := make([]byte, protocol.MaxWireDatagramSize+1)
	n, addr, err := c.packetConn.conn.ReadFromUDP(buffer)
	if err != nil {
		return protocol.VoicePacket{}, addr, normalizeReadError(err)
	}
	if n > protocol.MaxWireDatagramSize {
		return protocol.VoicePacket{}, addr, rejectedTooLarge(n)
	}

	ctx := protocol.DatagramContext{
		Direction: protocol.DirectionClientToServer,
		Endpoint:  addr.AddrPort(),
	}
	packet, err := c.packetConn.decode(ctx, buffer[:n])
	if err != nil {
		return protocol.VoicePacket{}, addr, err
	}
	return packet, addr, nil
}

func (c *ServerPacketConn) WritePacket(
	recipientID uint64,
	addr *net.UDPAddr,
	packet protocol.VoicePacket,
) error {
	if addr == nil {
		return errors.New("recipient UDP address is required")
	}
	ctx := protocol.DatagramContext{
		Direction:  protocol.DirectionServerToClient,
		KeyOwnerID: recipientID,
		Endpoint:   addr.AddrPort(),
	}
	return c.packetConn.write(ctx, packet, func(data []byte) error {
		_, err := c.packetConn.conn.WriteToUDP(data, addr)
		return err
	})
}

func (c *ServerPacketConn) SetReadDeadline(deadline time.Time) error {
	return c.packetConn.conn.SetReadDeadline(deadline)
}

func (c *ServerPacketConn) Close() error {
	return c.packetConn.close()
}

func (c *ServerPacketConn) LocalAddr() net.Addr {
	return c.packetConn.conn.LocalAddr()
}

func (c *packetConn) read(
	ctx protocol.DatagramContext,
	read func([]byte) (int, error),
) (protocol.VoicePacket, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	buffer := make([]byte, protocol.MaxWireDatagramSize+1)
	n, err := read(buffer)
	if err != nil {
		return protocol.VoicePacket{}, normalizeReadError(err)
	}
	if n > protocol.MaxWireDatagramSize {
		return protocol.VoicePacket{}, rejectedTooLarge(n)
	}
	return c.decode(ctx, buffer[:n])
}

func (c *packetConn) write(
	ctx protocol.DatagramContext,
	packet protocol.VoicePacket,
	write func([]byte) error,
) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
		return err
	}

	data, err := c.encode(ctx, packet)
	if err != nil {
		return err
	}
	if len(data) > protocol.MaxWireDatagramSize {
		return fmt.Errorf(
			"%w: got %d bytes, max %d",
			protocol.ErrPacketTooLarge,
			len(data),
			protocol.MaxWireDatagramSize,
		)
	}
	return write(data)
}

func (c *packetConn) encode(
	ctx protocol.DatagramContext,
	packet protocol.VoicePacket,
) ([]byte, error) {
	c.codecMu.Lock()
	defer c.codecMu.Unlock()
	return c.codec.Encode(ctx, packet)
}

func (c *packetConn) decode(
	ctx protocol.DatagramContext,
	datagram []byte,
) (protocol.VoicePacket, error) {
	c.codecMu.Lock()
	defer c.codecMu.Unlock()
	return c.codec.Decode(ctx, datagram)
}

func (c *packetConn) close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.conn.Close()
	})
	return c.closeErr
}

func normalizeReadError(err error) error {
	if isMessageTooLong(err) {
		return fmt.Errorf(
			"%w: %w: %v",
			protocol.ErrRejectedDatagram,
			protocol.ErrPacketTooLarge,
			err,
		)
	}
	return err
}

func rejectedTooLarge(size int) error {
	return fmt.Errorf(
		"%w: %w: got at least %d bytes, max %d",
		protocol.ErrRejectedDatagram,
		protocol.ErrPacketTooLarge,
		size,
		protocol.MaxWireDatagramSize,
	)
}

func addrPort(addr net.Addr) (netip.AddrPort, error) {
	udpAddr, ok := addr.(*net.UDPAddr)
	if !ok || udpAddr == nil {
		return netip.AddrPort{}, fmt.Errorf("expected UDP address, got %T", addr)
	}
	return udpAddr.AddrPort(), nil
}
