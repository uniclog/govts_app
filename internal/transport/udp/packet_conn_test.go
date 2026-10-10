package udp

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/protocol"
)

type codecCall struct {
	context protocol.DatagramContext
	packet  protocol.VoicePacket
}

type clientConnectionContract interface {
	BindSession(uint64) error
	ClearSession(uint64) error
	SendPacket(protocol.VoicePacket) error
	ReceivePacket() (protocol.VoicePacket, error)
	SetReadDeadline(time.Time) error
	Close() error
	LocalAddr() net.Addr
}

func TestClientPacketConnClearsOnlyExpectedSession(t *testing.T) {
	serverRaw, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer serverRaw.Close()
	clientRaw, err := net.DialUDP("udp4", nil, serverRaw.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := NewClientPacketConn(clientRaw, protocol.PlainDatagramCodec{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.BindSession(42); err != nil {
		t.Fatal(err)
	}
	if err := conn.ClearSession(41); err == nil {
		t.Fatal("ClearSession accepted the wrong session")
	}
	if err := conn.ClearSession(42); err != nil {
		t.Fatal(err)
	}
	if err := conn.BindSession(43); err != nil {
		t.Fatalf("rebind after clear: %v", err)
	}
}

type serverConnectionContract interface {
	ReadPacket() (protocol.VoicePacket, *net.UDPAddr, error)
	WritePacket(uint64, *net.UDPAddr, protocol.VoicePacket) error
	SetReadDeadline(time.Time) error
	Close() error
	LocalAddr() net.Addr
}

var (
	_ clientConnectionContract = (*ClientPacketConn)(nil)
	_ serverConnectionContract = (*ServerPacketConn)(nil)
)

type prefixCodec struct {
	encoded []codecCall
	decoded []protocol.DatagramContext
}

func (c *prefixCodec) Encode(
	ctx protocol.DatagramContext,
	packet protocol.VoicePacket,
) ([]byte, error) {
	data, err := protocol.EncodePacket(packet)
	if err != nil {
		return nil, err
	}
	c.encoded = append(c.encoded, codecCall{context: ctx, packet: packet})
	return append([]byte{0xa5}, data...), nil
}

func (c *prefixCodec) Decode(
	ctx protocol.DatagramContext,
	datagram []byte,
) (protocol.VoicePacket, error) {
	if len(datagram) == 0 || datagram[0] != 0xa5 {
		return protocol.VoicePacket{}, fmt.Errorf(
			"%w: missing codec prefix",
			protocol.ErrRejectedDatagram,
		)
	}
	c.decoded = append(c.decoded, ctx)
	return protocol.DecodePacket(datagram[1:])
}

func TestPacketConnectionsPassDirectionAndKeyOwnerToCodec(t *testing.T) {
	serverRaw, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	clientRaw, err := net.DialUDP("udp4", nil, serverRaw.LocalAddr().(*net.UDPAddr))
	if err != nil {
		_ = serverRaw.Close()
		t.Fatal(err)
	}

	clientCodec := &prefixCodec{}
	serverCodec := &prefixCodec{}
	clientConn, err := NewClientPacketConn(clientRaw, clientCodec)
	if err != nil {
		_ = clientRaw.Close()
		_ = serverRaw.Close()
		t.Fatal(err)
	}
	serverConn, err := NewServerPacketConn(serverRaw, serverCodec)
	if err != nil {
		_ = clientConn.Close()
		_ = serverRaw.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	const clientSessionID = 42
	if err := clientConn.BindSession(clientSessionID); err != nil {
		t.Fatal(err)
	}
	outbound := protocol.VoicePacket{
		Type:      protocol.PacketVoice,
		SessionID: clientSessionID,
		Sequence:  7,
		Payload:   []byte("client to server"),
	}
	if err := clientConn.SendPacket(outbound); err != nil {
		t.Fatal(err)
	}
	if err := serverConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	received, clientAddr, err := serverConn.ReadPacket()
	if err != nil {
		t.Fatal(err)
	}
	assertPacketEqual(t, received, outbound)
	assertContext(
		t,
		clientCodec.encoded[0].context,
		protocol.DirectionClientToServer,
		clientSessionID,
	)
	assertContext(
		t,
		serverCodec.decoded[0],
		protocol.DirectionClientToServer,
		0,
	)

	forwarded := protocol.VoicePacket{
		Type:      protocol.PacketVoice,
		SessionID: 99,
		Sequence:  8,
		Payload:   []byte("server to client"),
	}
	if err := serverConn.WritePacket(clientSessionID, clientAddr, forwarded); err != nil {
		t.Fatal(err)
	}
	if err := clientConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	received, err = clientConn.ReceivePacket()
	if err != nil {
		t.Fatal(err)
	}
	assertPacketEqual(t, received, forwarded)
	assertContext(
		t,
		serverCodec.encoded[0].context,
		protocol.DirectionServerToClient,
		clientSessionID,
	)
	if got := serverCodec.encoded[0].packet.SessionID; got != forwarded.SessionID {
		t.Fatalf("encoded sender ID = %d, want %d", got, forwarded.SessionID)
	}
	assertContext(
		t,
		clientCodec.decoded[0],
		protocol.DirectionServerToClient,
		clientSessionID,
	)
}

type oversizedCodec struct{}

func (oversizedCodec) Encode(
	protocol.DatagramContext,
	protocol.VoicePacket,
) ([]byte, error) {
	return make([]byte, protocol.MaxWireDatagramSize+1), nil
}

func (oversizedCodec) Decode(
	protocol.DatagramContext,
	[]byte,
) (protocol.VoicePacket, error) {
	return protocol.VoicePacket{}, nil
}

func TestPacketConnRejectsCodecOutputAboveWireLimit(t *testing.T) {
	raw, err := net.DialUDP("udp4", nil, &net.UDPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := NewClientPacketConn(raw, oversizedCodec{})
	if err != nil {
		_ = raw.Close()
		t.Fatal(err)
	}
	defer conn.Close()

	err = conn.SendPacket(protocol.VoicePacket{})
	if !errors.Is(err, protocol.ErrPacketTooLarge) {
		t.Fatalf("SendPacket() error = %v, want %v", err, protocol.ErrPacketTooLarge)
	}
}

func TestPacketConnConstructorsEnforceSocketMode(t *testing.T) {
	serverRaw, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer serverRaw.Close()

	if _, err := NewClientPacketConn(serverRaw, protocol.PlainDatagramCodec{}); err == nil {
		t.Fatal("NewClientPacketConn() accepted an unconnected socket")
	}

	clientRaw, err := net.DialUDP("udp4", nil, serverRaw.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer clientRaw.Close()
	if _, err := NewServerPacketConn(clientRaw, protocol.PlainDatagramCodec{}); err == nil {
		t.Fatal("NewServerPacketConn() accepted a connected socket")
	}
}

func TestClientPacketConnSessionBinding(t *testing.T) {
	serverRaw, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer serverRaw.Close()
	clientRaw, err := net.DialUDP("udp4", nil, serverRaw.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := NewClientPacketConn(clientRaw, protocol.PlainDatagramCodec{})
	if err != nil {
		_ = clientRaw.Close()
		t.Fatal(err)
	}
	defer conn.Close()

	voicePacket := protocol.VoicePacket{Type: protocol.PacketVoice, SessionID: 42}
	if err := conn.SendPacket(voicePacket); err == nil {
		t.Fatal("SendPacket() accepted a session packet before binding")
	}
	if err := conn.BindSession(0); err == nil {
		t.Fatal("BindSession() accepted session ID 0")
	}
	if err := conn.BindSession(42); err != nil {
		t.Fatal(err)
	}
	if err := conn.BindSession(42); err != nil {
		t.Fatalf("idempotent BindSession() error = %v", err)
	}
	if err := conn.BindSession(73); err == nil {
		t.Fatal("BindSession() replaced an active session")
	}
	voicePacket.SessionID = 73
	if err := conn.SendPacket(voicePacket); err == nil {
		t.Fatal("SendPacket() accepted a packet for a different session")
	}
}

func TestClientPacketConnCloseIsIdempotentAndUnblocksRead(t *testing.T) {
	serverRaw, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer serverRaw.Close()
	clientRaw, err := net.DialUDP("udp4", nil, serverRaw.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := NewClientPacketConn(clientRaw, protocol.PlainDatagramCodec{})
	if err != nil {
		_ = clientRaw.Close()
		t.Fatal(err)
	}

	resultCh := make(chan error, 1)
	go func() {
		_, err := conn.ReceivePacket()
		resultCh <- err
	}()

	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	select {
	case err := <-resultCh:
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("ReceivePacket() error = %v, want %v", err, net.ErrClosed)
		}
	case <-time.After(time.Second):
		t.Fatal("Close() did not unblock ReceivePacket()")
	}
}

func assertContext(
	t *testing.T,
	got protocol.DatagramContext,
	wantDirection protocol.PacketDirection,
	wantOwner uint64,
) {
	t.Helper()
	if got.Direction != wantDirection || got.KeyOwnerID != wantOwner {
		t.Fatalf(
			"datagram context = {direction:%d owner:%d}, want {%d %d}",
			got.Direction,
			got.KeyOwnerID,
			wantDirection,
			wantOwner,
		)
	}
	if !got.Endpoint.IsValid() {
		t.Fatal("datagram context has invalid endpoint")
	}
}

func assertPacketEqual(
	t *testing.T,
	got protocol.VoicePacket,
	want protocol.VoicePacket,
) {
	t.Helper()
	if got.Type != want.Type ||
		got.SessionID != want.SessionID ||
		got.Sequence != want.Sequence ||
		got.RequestID != want.RequestID ||
		!bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("decoded packet = %+v, want %+v", got, want)
	}
}
