package media

import (
	"log"
	"net"
	"sync"
	"sync/atomic"

	"github.com/pion/transport/v5"
	"github.com/pion/transport/v5/stdnet"
)

// Socket and resolver methods of stdnet.Net are stateless and are shared. Its
// interface list is a mutable cache filled once by NewNet, so every interface
// query takes a private snapshot: new ICE agents see current interfaces and
// concurrent PeerConnections never update shared state. Receive capacity is
// reserved for bursty keyframes before ICE starts reading the socket.
type bufferedMediaNet struct {
	*stdnet.Net
	observedReadBuffer atomic.Int64
	// Local ports of media sockets; the process sampler prunes closed ones.
	portsMu sync.Mutex
	ports   map[uint16]uint64 // port -> open sequence
	opened  uint64
}

var _ transport.Net = (*bufferedMediaNet)(nil)

func newBufferedMediaNet() (*bufferedMediaNet, error) {
	network, err := stdnet.NewNet()
	if err != nil {
		return nil, err
	}
	return &bufferedMediaNet{Net: network, ports: make(map[uint16]uint64)}, nil
}

func interfaceSnapshot() (*stdnet.Net, error) { return stdnet.NewNet() }

func (n *bufferedMediaNet) Interfaces() ([]*transport.Interface, error) {
	snapshot, err := interfaceSnapshot()
	if err != nil {
		return nil, err
	}
	return snapshot.Interfaces()
}

func (n *bufferedMediaNet) InterfaceByIndex(index int) (*transport.Interface, error) {
	snapshot, err := interfaceSnapshot()
	if err != nil {
		return nil, err
	}
	return snapshot.InterfaceByIndex(index)
}

func (n *bufferedMediaNet) InterfaceByName(name string) (*transport.Interface, error) {
	snapshot, err := interfaceSnapshot()
	if err != nil {
		return nil, err
	}
	return snapshot.InterfaceByName(name)
}

func (n *bufferedMediaNet) ListenUDP(network string, address *net.UDPAddr) (transport.UDPConn, error) {
	conn, err := n.Net.ListenUDP(network, address)
	if err != nil {
		return nil, err
	}
	if local, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		n.portsMu.Lock()
		n.opened++
		n.ports[uint16(local.Port)] = n.opened
		n.portsMu.Unlock()
	}
	// A buffer tuning failure should not prevent an otherwise usable media connection.
	if err := conn.SetReadBuffer(mediaReadBuffer); err != nil {
		log.Printf("screen UDP receive buffer: address=%s requested=%d error=%v", conn.LocalAddr(), mediaReadBuffer, err)
		return conn, nil
	}
	// SetReadBuffer succeeds even when Linux clamps the request to
	// net.core.rmem_max. Log the effective size once and on every change.
	if actual, ok := socketReadBuffer(conn); ok && n.observedReadBuffer.Swap(int64(actual)) != int64(actual) {
		log.Printf("screen UDP receive buffer: address=%s requested=%d effective=%d clamped=%t", conn.LocalAddr(), mediaReadBuffer, actual, actual < mediaReadBuffer)
	}
	return conn, nil
}

// mediaPorts returns a copy of the ports of sockets opened for media.
func (n *bufferedMediaNet) mediaPorts() map[uint16]uint64 {
	n.portsMu.Lock()
	defer n.portsMu.Unlock()
	ports := make(map[uint16]uint64, len(n.ports))
	for port, opened := range n.ports {
		ports[port] = opened
	}
	return ports
}

// forgetPorts drops ports no longer bound by this process, unless the port
// was reopened after the snapshot that found it closed.
func (n *bufferedMediaNet) forgetPorts(closed map[uint16]uint64) {
	n.portsMu.Lock()
	defer n.portsMu.Unlock()
	for port, opened := range closed {
		if n.ports[port] == opened {
			delete(n.ports, port)
		}
	}
}
