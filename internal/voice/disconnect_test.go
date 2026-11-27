package voice

import (
	"net"
	"testing"

	"uniclog.io/sonoryx/internal/protocol"
)

// The client sends Disconnect several times; only the first removes the
// session and the copies after it are not errors.
func TestRepeatedDisconnectIsIgnored(t *testing.T) {
	hub := NewHub()
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40001}
	session, _, err := hub.CreateAuthenticatedSession("alice", addr, 1, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	packet := protocol.VoicePacket{Type: protocol.PacketDisconnect, SessionID: session.ID}
	for copy := 0; copy < 3; copy++ {
		if err := HandleDisconnectPacket(hub, NewRequestCache(), packet, addr); err != nil {
			t.Fatalf("copy %d: %v", copy, err)
		}
	}
	if _, ok := hub.Get(session.ID); ok {
		t.Fatal("session survived Disconnect")
	}
}

func TestDisconnectFromAnotherAddressKeepsTheSession(t *testing.T) {
	hub := NewHub()
	addr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40002}
	session, _, err := hub.CreateAuthenticatedSession("alice", addr, 1, 0, 0, false)
	if err != nil {
		t.Fatal(err)
	}
	other := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 40003}
	packet := protocol.VoicePacket{Type: protocol.PacketDisconnect, SessionID: session.ID}
	if err := HandleDisconnectPacket(hub, NewRequestCache(), packet, other); err == nil {
		t.Fatal("Disconnect from another address was accepted")
	}
	if _, ok := hub.Get(session.ID); !ok {
		t.Fatal("session removed by a foreign Disconnect")
	}
}
