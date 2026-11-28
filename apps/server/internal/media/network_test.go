package media

import (
	"bytes"
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

func TestBufferedMediaNetUDPAndDeadlineRecovery(t *testing.T) {
	network, err := newBufferedMediaNet()
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := network.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = receiver.Close() })
	address := receiver.LocalAddr().(*net.UDPAddr)
	if network.mediaPorts()[uint16(address.Port)] == 0 {
		t.Fatal("media socket port was not registered")
	}
	sender, err := net.DialUDP("udp4", nil, address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sender.Close() })
	if err := receiver.SetReadDeadline(time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1500)
	if _, _, err := receiver.ReadFrom(buffer); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("expired read deadline: got %v", err)
	}
	if err := receiver.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	payload := []byte("media UDP after deadline reset")
	if _, err := sender.Write(payload); err != nil {
		t.Fatal(err)
	}
	n, source, err := receiver.ReadFrom(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buffer[:n], payload) {
		t.Fatalf("received corrupted payload: %q", buffer[:n])
	}
	if _, err := receiver.WriteTo(buffer[:n], source); err != nil {
		t.Fatal(err)
	}
	if err := sender.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	n, err = sender.Read(buffer)
	if err != nil || !bytes.Equal(buffer[:n], payload) {
		t.Fatalf("UDP reply: payload=%q error=%v", buffer[:n], err)
	}
}
