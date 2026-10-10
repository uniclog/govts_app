package udp

import (
	"errors"
	"net"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/protocol"
)

func TestReadVoicePacketRejectsOversizedDatagram(t *testing.T) {
	serverConn, err := net.ListenUDP("udp4", &net.UDPAddr{
		IP: net.ParseIP("127.0.0.1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	packetConn, err := NewServerPacketConn(serverConn, protocol.PlainDatagramCodec{})
	if err != nil {
		_ = serverConn.Close()
		t.Fatal(err)
	}
	defer packetConn.Close()

	clientConn, err := net.DialUDP(
		"udp4",
		nil,
		packetConn.LocalAddr().(*net.UDPAddr),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer clientConn.Close()

	if _, err := clientConn.Write(make([]byte, protocol.MaxWireDatagramSize+100)); err != nil {
		t.Fatal(err)
	}
	if err := packetConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	_, _, err = packetConn.ReadPacket()
	if !errors.Is(err, protocol.ErrPacketTooLarge) {
		t.Fatalf("ReadVoicePacket() error = %v, want %v", err, protocol.ErrPacketTooLarge)
	}
	if !errors.Is(err, protocol.ErrRejectedDatagram) {
		t.Fatalf("ReadVoicePacket() error = %v, want rejected datagram", err)
	}
}
