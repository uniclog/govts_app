package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/audio"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

type joinTestPeer struct {
	serverConn *udp.ServerPacketConn
	clientConn *udp.ClientPacketConn
	state      *State
	ctx        context.Context
}

func newJoinTestPeer(t *testing.T) *joinTestPeer {
	t.Helper()

	serverConn, err := net.ListenUDP("udp4", &net.UDPAddr{
		IP: net.ParseIP("127.0.0.1"),
	})
	if err != nil {
		t.Fatal(err)
	}

	clientConn, err := net.DialUDP(
		"udp4",
		nil,
		serverConn.LocalAddr().(*net.UDPAddr),
	)
	if err != nil {
		_ = serverConn.Close()
		t.Fatal(err)
	}
	serverPacketConn := mustServerPacketConn(t, serverConn)
	clientPacketConn := mustClientPacketConn(t, clientConn)
	if err := clientPacketConn.BindSession(42); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	state := NewState(42, "alice")
	encodedCh := make(chan audio.MediaFrame, 1)
	controlCh := make(chan protocol.VoicePacket, 4)

	go func() {
		_ = ReceiveLoop(ctx, clientPacketConn, encodedCh, controlCh)
	}()
	go func() {
		_ = ControlLoop(ctx, state, controlCh)
	}()

	t.Cleanup(func() {
		cancel()
		_ = clientPacketConn.Close()
		_ = serverPacketConn.Close()
	})

	return &joinTestPeer{
		serverConn: serverPacketConn,
		clientConn: clientPacketConn,
		state:      state,
		ctx:        ctx,
	}
}

func (p *joinTestPeer) receiveRequest() (protocol.VoicePacket, *net.UDPAddr, error) {
	if err := p.serverConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		return protocol.VoicePacket{}, nil, err
	}
	return p.serverConn.ReadPacket()
}

func (p *joinTestPeer) sendResponse(
	addr *net.UDPAddr,
	response protocol.VoicePacket,
) error {
	return p.serverConn.WritePacket(response.SessionID, addr, response)
}

func TestJoinChannelRejectsZeroID(t *testing.T) {
	state := NewState(42, "alice")

	err := JoinChannel(context.Background(), nil, state, 0)
	if err == nil {
		t.Fatal("JoinChannel() error = nil, want empty channel error")
	}
	if !strings.Contains(err.Error(), "must not be zero") {
		t.Fatalf("JoinChannel() error = %q, want zero ID error", err)
	}
}

func TestPerformHandshakeRetriesWithSameRequestID(t *testing.T) {
	serverConn, clientConn := newHandshakeTestConnections(t)
	serverErrCh := make(chan error, 1)

	go func() {
		if err := serverConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			serverErrCh <- err
			return
		}
		first, _, err := serverConn.ReadPacket()
		if err != nil {
			serverErrCh <- err
			return
		}
		second, addr, err := serverConn.ReadPacket()
		if err != nil {
			serverErrCh <- err
			return
		}
		if second.RequestID != first.RequestID {
			serverErrCh <- fmt.Errorf(
				"retry RequestID = %d, want %d",
				second.RequestID,
				first.RequestID,
			)
			return
		}
		serverErrCh <- serverConn.WritePacket(99, addr, protocol.VoicePacket{
			Type:      protocol.PacketHelloAck,
			SessionID: 99,
			RequestID: second.RequestID,
			Sequence:  1 << 16,
		})
	}()

	sessionID, err := performHandshakeWithRequestID(
		context.Background(),
		clientConn,
		"alice",
		17,
		20*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	if serverErr := <-serverErrCh; serverErr != nil {
		t.Fatal(serverErr)
	}
	if sessionID != 99 {
		t.Fatalf("session ID = %d, want 99", sessionID)
	}
}

func TestPerformHandshakeFinalTimeout(t *testing.T) {
	serverConn, clientConn := newHandshakeTestConnections(t)
	serverErrCh := make(chan error, 1)

	go func() {
		if err := serverConn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
			serverErrCh <- err
			return
		}
		for range requestAttempts {
			if _, _, err := serverConn.ReadPacket(); err != nil {
				serverErrCh <- err
				return
			}
		}
		serverErrCh <- nil
	}()

	_, err := performHandshakeWithRequestID(
		context.Background(),
		clientConn,
		"alice",
		17,
		10*time.Millisecond,
	)
	if err == nil || !strings.Contains(err.Error(), "timed out after 3 attempts") {
		t.Fatalf("performHandshakeWithRequestID() error = %v, want final timeout", err)
	}
	if serverErr := <-serverErrCh; serverErr != nil {
		t.Fatal(serverErr)
	}
}

func TestPerformHandshakeRejectsServerBelowMinimum(t *testing.T) {
	serverConn, clientConn := newHandshakeTestConnections(t)
	serverErr := make(chan error, 1)
	go func() {
		request, addr, err := serverConn.ReadPacket()
		if err != nil {
			serverErr <- err
			return
		}
		if request.Sequence != 2<<16 {
			serverErr <- fmt.Errorf("minimum = %x, want 0.2.0", request.Sequence)
			return
		}
		serverErr <- serverConn.WritePacket(0, addr, protocol.VoicePacket{Type: protocol.PacketServerVersionTooOld, RequestID: request.RequestID, Sequence: 1 << 16})
	}()
	_, err := PerformHandshakeAttempts(context.Background(), clientConn, "alice", time.Second, 1, "0.2.0")
	var versionErr *ServerVersionTooOldError
	if !errors.As(err, &versionErr) || versionErr.Server.String() != "0.1.0" || versionErr.Required.String() != "0.2.0" {
		t.Fatalf("version error = %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestPerformHandshakeSkipsRejectedDatagram(t *testing.T) {
	serverRaw, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	clientRaw, err := net.DialUDP("udp4", nil, serverRaw.LocalAddr().(*net.UDPAddr))
	if err != nil {
		_ = serverRaw.Close()
		t.Fatal(err)
	}
	serverConn := mustServerPacketConn(t, serverRaw)
	clientConn, err := udp.NewClientPacketConn(clientRaw, &rejectOnceCodec{})
	if err != nil {
		_ = clientRaw.Close()
		_ = serverConn.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	serverErrCh := make(chan error, 1)
	go func() {
		request, addr, err := serverConn.ReadPacket()
		if err != nil {
			serverErrCh <- err
			return
		}
		response := protocol.VoicePacket{
			Type:      protocol.PacketHelloAck,
			SessionID: 99,
			RequestID: request.RequestID,
			Sequence:  1 << 16,
		}
		if err := serverConn.WritePacket(99, addr, response); err != nil {
			serverErrCh <- err
			return
		}
		serverErrCh <- serverConn.WritePacket(99, addr, response)
	}()

	sessionID, err := performHandshakeWithRequestID(
		context.Background(),
		clientConn,
		"alice",
		17,
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if sessionID != 99 {
		t.Fatalf("session ID = %d, want 99", sessionID)
	}
	if err := <-serverErrCh; err != nil {
		t.Fatal(err)
	}
}

func TestPerformHandshakeSkipsStaleControlPacket(t *testing.T) {
	serverConn, clientConn := newHandshakeTestConnections(t)
	serverErr := make(chan error, 1)
	go func() {
		request, addr, err := serverConn.ReadPacket()
		if err != nil {
			serverErr <- err
			return
		}
		if err := serverConn.WritePacket(88, addr, protocol.VoicePacket{Type: protocol.PacketJoinChannelAck, SessionID: 88, RequestID: request.RequestID + 1}); err != nil {
			serverErr <- err
			return
		}
		serverErr <- serverConn.WritePacket(99, addr, protocol.VoicePacket{Type: protocol.PacketHelloAck, SessionID: 99, RequestID: request.RequestID, Sequence: 1 << 16})
	}()
	sessionID, err := performHandshakeWithRequestID(context.Background(), clientConn, "alice", 17, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if sessionID != 99 {
		t.Fatalf("session ID = %d", sessionID)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestHeartbeatProbeHandlesAckAndInvalidSession(t *testing.T) {
	for _, packetType := range []uint8{protocol.PacketHeartbeatAck, protocol.PacketSessionInvalid} {
		t.Run(fmt.Sprintf("type-%d", packetType), func(t *testing.T) {
			peer := newJoinTestPeer(t)
			serverErr := make(chan error, 1)
			go func() {
				request, addr, err := peer.receiveRequest()
				if err != nil {
					serverErr <- err
					return
				}
				serverErr <- peer.sendResponse(addr, protocol.VoicePacket{Type: packetType, SessionID: request.SessionID, RequestID: request.RequestID})
			}()
			err := heartbeatProbe(peer.ctx, peer.clientConn, peer.state, time.Second)
			if packetType == protocol.PacketHeartbeatAck && err != nil {
				t.Fatal(err)
			}
			if packetType == protocol.PacketHeartbeatAck && peer.state.LastHeartbeatAck().IsZero() {
				t.Fatal("last heartbeat ACK time was not recorded")
			}
			if packetType == protocol.PacketSessionInvalid && !errors.Is(err, ErrConnectionLost) {
				t.Fatalf("heartbeat error = %v", err)
			}
			if err := <-serverErr; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHeartbeatProbeRecordsServerVoiceLoss(t *testing.T) {
	peer := newJoinTestPeer(t)
	peer.state.SetConnectionStatus(ConnectionConnected)
	for i := uint32(1); i <= 8; i++ {
		peer.state.RecordVoiceSent(i)
	}
	serverErr := make(chan error, 1)
	go func() {
		request, addr, err := peer.receiveRequest()
		if err != nil {
			serverErr <- err
			return
		}
		if request.Sequence != 8<<14|8 {
			serverErr <- fmt.Errorf("window range = %x, want count 8 ending at 8", request.Sequence)
			return
		}
		serverErr <- peer.sendResponse(addr, protocol.VoicePacket{Type: protocol.PacketHeartbeatAck, SessionID: request.SessionID, RequestID: request.RequestID, Sequence: 1250})
	}()
	if err := heartbeatProbe(peer.ctx, peer.clientConn, peer.state, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	if got := peer.state.ConnectionStats(); !got.OutgoingKnown || got.OutgoingLoss != 12.5 {
		t.Fatalf("outgoing stats = %+v", got)
	}
}

func TestHeartbeatProbeTimeoutMeansConnectionLost(t *testing.T) {
	peer := newJoinTestPeer(t)
	err := heartbeatProbe(peer.ctx, peer.clientConn, peer.state, 10*time.Millisecond)
	if !errors.Is(err, ErrConnectionLost) {
		t.Fatalf("heartbeat error = %v", err)
	}
	if strings.Count(err.Error(), ErrConnectionLost.Error()) != 1 {
		t.Fatalf("heartbeat error repeats connection state: %q", err)
	}
	if !strings.Contains(err.Error(), "after 3 attempts") {
		t.Fatalf("heartbeat error does not report retries: %q", err)
	}
}

func TestHeartbeatProbeRetriesDroppedRequest(t *testing.T) {
	peer := newJoinTestPeer(t)
	serverErr := make(chan error, 1)
	go func() {
		first, _, err := peer.receiveRequest()
		if err != nil {
			serverErr <- err
			return
		}
		second, addr, err := peer.receiveRequest()
		if err != nil {
			serverErr <- err
			return
		}
		if second.RequestID != first.RequestID {
			serverErr <- fmt.Errorf("retry request ID = %d, want %d", second.RequestID, first.RequestID)
			return
		}
		serverErr <- peer.sendResponse(addr, protocol.VoicePacket{Type: protocol.PacketHeartbeatAck, SessionID: second.SessionID, RequestID: second.RequestID})
	}()
	if err := heartbeatProbe(peer.ctx, peer.clientConn, peer.state, 300*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestHeartbeatProbeRejectsMalformedLifecyclePayload(t *testing.T) {
	for _, packetType := range []uint8{protocol.PacketHeartbeatAck, protocol.PacketSessionInvalid} {
		t.Run(fmt.Sprintf("type-%d", packetType), func(t *testing.T) {
			peer := newJoinTestPeer(t)
			serverErr := make(chan error, 1)
			go func() {
				request, addr, err := peer.receiveRequest()
				if err != nil {
					serverErr <- err
					return
				}
				serverErr <- peer.sendResponse(addr, protocol.VoicePacket{
					Type:      packetType,
					SessionID: request.SessionID,
					RequestID: request.RequestID,
					Payload:   []byte{1},
				})
			}()
			err := heartbeatProbe(peer.ctx, peer.clientConn, peer.state, time.Second)
			if !errors.Is(err, ErrConnectionLost) || !strings.Contains(err.Error(), "payload") {
				t.Fatalf("heartbeat error = %v", err)
			}
			if err := <-serverErr; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestHeartbeatProbeIgnoresWrongRequestAndDuplicateAck(t *testing.T) {
	peer := newJoinTestPeer(t)
	serverErr := make(chan error, 1)
	go func() {
		request, addr, err := peer.receiveRequest()
		if err != nil {
			serverErr <- err
			return
		}
		wrong := protocol.VoicePacket{Type: protocol.PacketHeartbeatAck, SessionID: request.SessionID, RequestID: request.RequestID + 1}
		if err := peer.sendResponse(addr, wrong); err != nil {
			serverErr <- err
			return
		}
		wrong.SessionID++
		wrong.RequestID = request.RequestID
		if err := peer.sendResponse(addr, wrong); err != nil {
			serverErr <- err
			return
		}
		ack := protocol.VoicePacket{Type: protocol.PacketHeartbeatAck, SessionID: request.SessionID, RequestID: request.RequestID}
		if err := peer.sendResponse(addr, ack); err != nil {
			serverErr <- err
			return
		}
		serverErr <- peer.sendResponse(addr, ack)
	}()
	if err := heartbeatProbe(peer.ctx, peer.clientConn, peer.state, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestLateHeartbeatAckCompletesRetryWindow(t *testing.T) {
	peer := newJoinTestPeer(t)
	serverErr := make(chan error, 1)
	go func() {
		request, addr, err := peer.receiveRequest()
		if err != nil {
			serverErr <- err
			return
		}
		time.Sleep(15 * time.Millisecond)
		serverErr <- peer.sendResponse(addr, protocol.VoicePacket{
			Type:      protocol.PacketHeartbeatAck,
			SessionID: request.SessionID,
			RequestID: request.RequestID,
		})
	}()
	if err := heartbeatProbe(peer.ctx, peer.clientConn, peer.state, 30*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	if peer.state.LastHeartbeatAck().IsZero() {
		t.Fatal("heartbeat ACK received during retry window did not restore liveness")
	}
}

func TestHeartbeatLoopRejectsInvalidTiming(t *testing.T) {
	state := NewState(1, "alice")
	for _, timing := range []struct{ interval, deadline time.Duration }{{0, time.Second}, {time.Second, 0}} {
		if err := heartbeatLoop(context.Background(), nil, state, timing.interval, timing.deadline); err == nil {
			t.Fatal("heartbeat loop accepted non-positive timing")
		}
	}
}

func newHandshakeTestConnections(
	t *testing.T,
) (*udp.ServerPacketConn, *udp.ClientPacketConn) {
	t.Helper()

	serverConn, err := net.ListenUDP("udp4", &net.UDPAddr{
		IP: net.ParseIP("127.0.0.1"),
	})
	if err != nil {
		t.Fatal(err)
	}

	clientConn, err := net.DialUDP(
		"udp4",
		nil,
		serverConn.LocalAddr().(*net.UDPAddr),
	)
	if err != nil {
		_ = serverConn.Close()
		t.Fatal(err)
	}
	serverPacketConn := mustServerPacketConn(t, serverConn)
	clientPacketConn := mustClientPacketConn(t, clientConn)

	t.Cleanup(func() {
		_ = clientPacketConn.Close()
		_ = serverPacketConn.Close()
	})
	return serverPacketConn, clientPacketConn
}

func mustClientPacketConn(t *testing.T, conn *net.UDPConn) *udp.ClientPacketConn {
	t.Helper()

	packetConn, err := udp.NewClientPacketConn(conn, protocol.PlainDatagramCodec{})
	if err != nil {
		t.Fatalf("NewClientPacketConn() error = %v", err)
	}
	return packetConn
}

func mustServerPacketConn(t *testing.T, conn *net.UDPConn) *udp.ServerPacketConn {
	t.Helper()

	packetConn, err := udp.NewServerPacketConn(conn, protocol.PlainDatagramCodec{})
	if err != nil {
		t.Fatalf("NewServerPacketConn() error = %v", err)
	}
	return packetConn
}

func TestReceiveLoopPreservesMediaStreamIdentity(t *testing.T) {
	serverConn, clientConn := newHandshakeTestConnections(t)
	if err := clientConn.BindSession(42); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	encodedCh := make(chan audio.MediaFrame, 1)
	controlCh := make(chan protocol.VoicePacket, 1)
	resultCh := make(chan error, 1)

	go func() {
		resultCh <- ReceiveLoop(ctx, clientConn, encodedCh, controlCh)
	}()

	want := protocol.VoicePacket{
		Type:      protocol.PacketVoice,
		SessionID: 73,
		Sequence:  19,
		Payload:   []byte("encoded opus frame"),
	}
	clientAddr := clientConn.LocalAddr().(*net.UDPAddr)
	if err := serverConn.WritePacket(42, clientAddr, want); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-encodedCh:
		if got.SenderID != want.SessionID {
			t.Errorf("MediaFrame.SenderID = %d, want %d", got.SenderID, want.SessionID)
		}
		if got.Sequence != want.Sequence {
			t.Errorf("MediaFrame.Sequence = %d, want %d", got.Sequence, want.Sequence)
		}
		if string(got.Data) != string(want.Payload) {
			t.Errorf("MediaFrame.Data = %q, want %q", got.Data, want.Payload)
		}
	case <-time.After(time.Second):
		t.Fatal("ReceiveLoop() did not emit media frame")
	}

	cancel()
	select {
	case err := <-resultCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ReceiveLoop() error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		t.Fatal("ReceiveLoop() did not stop after cancellation")
	}
}

type rejectOnceCodec struct {
	rejected bool
}

func (c *rejectOnceCodec) Encode(
	ctx protocol.DatagramContext,
	packet protocol.VoicePacket,
) ([]byte, error) {
	return (protocol.PlainDatagramCodec{}).Encode(ctx, packet)
}

func (c *rejectOnceCodec) Decode(
	ctx protocol.DatagramContext,
	datagram []byte,
) (protocol.VoicePacket, error) {
	if !c.rejected {
		c.rejected = true
		return protocol.VoicePacket{}, fmt.Errorf(
			"%w: simulated authentication failure",
			protocol.ErrRejectedDatagram,
		)
	}
	return (protocol.PlainDatagramCodec{}).Decode(ctx, datagram)
}

func TestReceiveLoopSkipsRejectedDatagram(t *testing.T) {
	serverRaw, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	clientRaw, err := net.DialUDP("udp4", nil, serverRaw.LocalAddr().(*net.UDPAddr))
	if err != nil {
		_ = serverRaw.Close()
		t.Fatal(err)
	}

	serverConn := mustServerPacketConn(t, serverRaw)
	clientConn, err := udp.NewClientPacketConn(clientRaw, &rejectOnceCodec{})
	if err != nil {
		_ = clientRaw.Close()
		_ = serverConn.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})
	if err := clientConn.BindSession(42); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	encodedCh := make(chan audio.MediaFrame, 1)
	controlCh := make(chan protocol.VoicePacket, 1)
	resultCh := make(chan error, 1)
	go func() {
		resultCh <- ReceiveLoop(ctx, clientConn, encodedCh, controlCh)
	}()

	clientAddr := clientConn.LocalAddr().(*net.UDPAddr)
	for sequence := uint32(1); sequence <= 2; sequence++ {
		if err := serverConn.WritePacket(42, clientAddr, protocol.VoicePacket{
			Type:      protocol.PacketVoice,
			SessionID: 73,
			Sequence:  sequence,
			Payload:   []byte{byte(sequence)},
		}); err != nil {
			t.Fatal(err)
		}
	}

	select {
	case frame := <-encodedCh:
		if frame.Sequence != 2 {
			t.Fatalf("received sequence = %d, want 2", frame.Sequence)
		}
	case err := <-resultCh:
		t.Fatalf("ReceiveLoop() stopped after rejected datagram: %v", err)
	case <-time.After(time.Second):
		t.Fatal("ReceiveLoop() did not receive packet after rejected datagram")
	}

	cancel()
	select {
	case err := <-resultCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ReceiveLoop() error = %v, want %v", err, context.Canceled)
		}
	case <-time.After(time.Second):
		t.Fatal("ReceiveLoop() did not stop")
	}
}

func TestJoinChannelSuccess(t *testing.T) {
	peer := newJoinTestPeer(t)
	serverErrCh := make(chan error, 1)

	go func() {
		request, addr, err := peer.receiveRequest()
		if err != nil {
			serverErrCh <- err
			return
		}
		payload, err := protocol.EncodeJoinChannelAck(7, 2)
		if err != nil {
			serverErrCh <- err
			return
		}
		serverErrCh <- peer.sendResponse(addr, protocol.VoicePacket{
			Type:      protocol.PacketJoinChannelAck,
			SessionID: request.SessionID,
			RequestID: request.RequestID,
			Payload:   payload,
		})
	}()

	if err := JoinChannel(peer.ctx, peer.clientConn, peer.state, 7); err != nil {
		t.Fatal(err)
	}
	if err := <-serverErrCh; err != nil {
		t.Fatal(err)
	}
	if got := peer.state.ChannelID(); got != 7 {
		t.Fatalf("State.ChannelID() = %d, want 7", got)
	}
}

func TestJoinChannelServerError(t *testing.T) {
	peer := newJoinTestPeer(t)
	serverErrCh := make(chan error, 1)

	go func() {
		request, addr, err := peer.receiveRequest()
		if err != nil {
			serverErrCh <- err
			return
		}
		serverErrCh <- peer.sendResponse(
			addr,
			protocol.NewErrorPacket(
				request.SessionID,
				request.RequestID,
				"channel is unavailable",
			),
		)
	}()

	err := JoinChannel(peer.ctx, peer.clientConn, peer.state, 7)
	if err == nil || !strings.Contains(err.Error(), "channel is unavailable") {
		t.Fatalf("JoinChannel() error = %v, want server error", err)
	}
	if serverErr := <-serverErrCh; serverErr != nil {
		t.Fatal(serverErr)
	}
	if got := peer.state.ChannelID(); got != 0 {
		t.Fatalf("State.ChannelID() = %d after server error, want zero", got)
	}
}

func TestJoinChannelRetriesLostAcknowledgement(t *testing.T) {
	peer := newJoinTestPeer(t)
	serverErrCh := make(chan error, 1)

	go func() {
		first, _, err := peer.receiveRequest()
		if err != nil {
			serverErrCh <- err
			return
		}

		second, addr, err := peer.receiveRequest()
		if err != nil {
			serverErrCh <- err
			return
		}
		if second.RequestID != first.RequestID {
			serverErrCh <- fmt.Errorf(
				"retry RequestID = %d, want %d",
				second.RequestID,
				first.RequestID,
			)
			return
		}

		payload, err := protocol.EncodeJoinChannelAck(7, 2)
		if err != nil {
			serverErrCh <- err
			return
		}
		serverErrCh <- peer.sendResponse(addr, protocol.VoicePacket{
			Type:      protocol.PacketJoinChannelAck,
			SessionID: second.SessionID,
			RequestID: second.RequestID,
			Payload:   payload,
		})
	}()

	err := joinChannelWithTimeout(
		peer.ctx,
		peer.clientConn,
		peer.state,
		7,
		20*time.Millisecond,
	)
	if err != nil {
		t.Fatal(err)
	}
	if serverErr := <-serverErrCh; serverErr != nil {
		t.Fatal(serverErr)
	}
}

func TestJoinChannelFinalTimeout(t *testing.T) {
	peer := newJoinTestPeer(t)
	serverErrCh := make(chan error, 1)

	go func() {
		for range 3 {
			if _, _, err := peer.receiveRequest(); err != nil {
				serverErrCh <- err
				return
			}
		}
		serverErrCh <- nil
	}()

	err := joinChannelWithTimeout(
		peer.ctx,
		peer.clientConn,
		peer.state,
		7,
		10*time.Millisecond,
	)
	if err == nil || !strings.Contains(err.Error(), "timed out after 3 attempts") {
		t.Fatalf("JoinChannel() error = %v, want final timeout", err)
	}
	if serverErr := <-serverErrCh; serverErr != nil {
		t.Fatal(serverErr)
	}
	if completed := peer.state.CompleteRequest(ControlResponse{
		Type:      protocol.PacketJoinChannelAck,
		RequestID: 1,
		Payload:   mustJoinAckPayload(t, 7),
	}); completed {
		t.Fatal("late acknowledgement completed an already timed-out request")
	}
}

func TestValidateJoinResponse(t *testing.T) {
	tests := []struct {
		name      string
		response  ControlResponse
		requested domain.ChannelID
		wantError string
	}{
		{
			name: "matching acknowledgement",
			response: ControlResponse{
				Type:    protocol.PacketJoinChannelAck,
				Payload: mustJoinAckPayload(t, 7),
			},
			requested: 7,
		},
		{
			name: "unexpected response type",
			response: ControlResponse{
				Type: protocol.PacketHeartbeat,
			},
			requested: 7,
			wantError: "unexpected join response",
		},
		{
			name: "different channel",
			response: ControlResponse{
				Type:    protocol.PacketJoinChannelAck,
				Payload: mustJoinAckPayload(t, 8),
			},
			requested: 7,
			wantError: "channel mismatch",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateJoinResponse(tt.response, tt.requested)
			if tt.wantError == "" {
				if err != nil {
					t.Fatalf("validateJoinResponse() error = %v", err)
				}
				return
			}

			if err == nil {
				t.Fatalf("validateJoinResponse() error = nil, want %q", tt.wantError)
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("validateJoinResponse() error = %q, want substring %q", err, tt.wantError)
			}
		})
	}
}

func mustJoinAckPayload(t *testing.T, channelID domain.ChannelID) []byte {
	t.Helper()
	payload, err := protocol.EncodeJoinChannelAck(channelID, 2)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}
