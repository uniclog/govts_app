package udp

import (
	"errors"
	"net"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/protocol"
)

func TestReceivePacketRejectsOversizedDatagram(t *testing.T) {
	serverConn, err := net.ListenUDP("udp4", &net.UDPAddr{
		IP: net.ParseIP("127.0.0.1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer serverConn.Close()

	clientConn, err := net.DialUDP(
		"udp4",
		nil,
		serverConn.LocalAddr().(*net.UDPAddr),
	)
	if err != nil {
		t.Fatal(err)
	}
	packetConn, err := NewClientPacketConn(clientConn, protocol.PlainDatagramCodec{})
	if err != nil {
		_ = clientConn.Close()
		t.Fatal(err)
	}
	defer packetConn.Close()

	clientAddr := packetConn.LocalAddr().(*net.UDPAddr)
	if _, err := serverConn.WriteToUDP(
		make([]byte, protocol.MaxWireDatagramSize+100),
		clientAddr,
	); err != nil {
		t.Fatal(err)
	}
	if err := packetConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	_, err = packetConn.ReceivePacket()
	if !errors.Is(err, protocol.ErrPacketTooLarge) {
		t.Fatalf("ReceivePacket() error = %v, want %v", err, protocol.ErrPacketTooLarge)
	}
	if !errors.Is(err, protocol.ErrRejectedDatagram) {
		t.Fatalf("ReceivePacket() error = %v, want rejected datagram", err)
	}
}
