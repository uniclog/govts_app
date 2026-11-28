package client

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	voice "uniclog.io/sonoryx/apps/server/testsupport/voice"
	"uniclog.io/sonoryx/internal/protocol"
)

func TestHeartbeatLoopSurvivesOneMissedProbe(t *testing.T) {
	peer := newJoinTestPeer(t)
	ctx, cancel := context.WithCancel(peer.ctx)
	defer cancel()
	serverErr := make(chan error, 1)
	go func() {
		var dropped uint32
		acks := 0
		for {
			request, addr, err := peer.receiveRequest()
			if err != nil {
				serverErr <- err
				return
			}
			// Drop every retry of the first probe, then answer the next ones.
			if dropped == 0 || request.RequestID == dropped {
				dropped = request.RequestID
				continue
			}
			if err := peer.sendResponse(addr, protocol.VoicePacket{Type: protocol.PacketHeartbeatAck, SessionID: request.SessionID, RequestID: request.RequestID}); err != nil {
				serverErr <- err
				return
			}
			if acks++; acks == 3 {
				cancel()
				serverErr <- nil
				return
			}
		}
	}()
	err := heartbeatLoop(ctx, peer.clientConn, peer.state, 50*time.Millisecond, 300*time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("heartbeat loop error = %v, want cancellation after recovery", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestHeartbeatLoopResetsMissesAfterAck(t *testing.T) {
	peer := newJoinTestPeer(t)
	ctx, cancel := context.WithCancel(peer.ctx)
	defer cancel()
	serverErr := make(chan error, 1)
	go func() {
		// Requests of the odd probes stay unanswered: misses never run in a row.
		probe, last := 0, uint32(0)
		for {
			request, addr, err := peer.receiveRequest()
			if err != nil {
				serverErr <- err
				return
			}
			if request.RequestID != last {
				last = request.RequestID
				probe++
			}
			if probe%2 == 1 {
				continue
			}
			if err := peer.sendResponse(addr, protocol.VoicePacket{Type: protocol.PacketHeartbeatAck, SessionID: request.SessionID, RequestID: request.RequestID}); err != nil {
				serverErr <- err
				return
			}
			if probe == 6 {
				cancel()
				serverErr <- nil
				return
			}
		}
	}()
	err := heartbeatLoop(ctx, peer.clientConn, peer.state, 20*time.Millisecond, 150*time.Millisecond)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("heartbeat loop error = %v, want cancellation", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestHeartbeatLoopReportsLossAfterConsecutiveMisses(t *testing.T) {
	peer := newJoinTestPeer(t)
	const deadline = 60 * time.Millisecond
	started := time.Now()
	err := heartbeatLoop(peer.ctx, peer.clientConn, peer.state, 10*time.Millisecond, deadline)
	if !errors.Is(err, ErrConnectionLost) || !errors.Is(err, errNoReply) {
		t.Fatalf("heartbeat loop error = %v", err)
	}
	if elapsed := time.Since(started); elapsed < heartbeatMaxMisses*deadline {
		t.Fatalf("connection lost after %s, before %d misses", elapsed, heartbeatMaxMisses)
	}
}

func TestHeartbeatLoopEndsAtOnceWhenServerRejectsSession(t *testing.T) {
	peer := newJoinTestPeer(t)
	serverErr := make(chan error, 1)
	go func() {
		request, addr, err := peer.receiveRequest()
		if err != nil {
			serverErr <- err
			return
		}
		serverErr <- peer.sendResponse(addr, protocol.VoicePacket{Type: protocol.PacketSessionInvalid, SessionID: request.SessionID, RequestID: request.RequestID})
	}()
	// A second probe would have to wait for this interval.
	err := heartbeatLoop(peer.ctx, peer.clientConn, peer.state, time.Hour, time.Second)
	if !errors.Is(err, ErrConnectionLost) || errors.Is(err, errNoReply) {
		t.Fatalf("heartbeat loop error = %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestRequestErrorSeparatesSilenceFromClosedSocket(t *testing.T) {
	peer := newJoinTestPeer(t)
	_, err := doRequestAttempts(peer.ctx, peer.clientConn, peer.state, protocol.VoicePacket{Type: protocol.PacketHeartbeat}, 5*time.Millisecond, 2)
	if !errors.Is(err, errNoReply) {
		t.Fatalf("timeout error = %v, want errNoReply", err)
	}
	if err := peer.clientConn.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = doRequestAttempts(context.Background(), peer.clientConn, peer.state, protocol.VoicePacket{Type: protocol.PacketHeartbeat}, 5*time.Millisecond, 2)
	if !errors.Is(err, net.ErrClosed) || errors.Is(err, errNoReply) {
		t.Fatalf("closed socket error = %v", err)
	}
}

func TestStateSyncerLeavesSilenceToHeartbeat(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for all snapshot request attempts")
	}
	t.Parallel()
	peer := newJoinTestPeer(t)
	ctx, cancel := context.WithCancel(peer.ctx)
	defer cancel()
	syncer := &StateSyncer{Conn: peer.clientConn, State: peer.state}
	done := make(chan error, 1)
	go func() { done <- syncer.run(ctx, 10*time.Millisecond) }()
	// One request with all its attempts takes requestAttempts*timeout.
	select {
	case err := <-done:
		t.Fatalf("state sync ended without a server reply: %v", err)
	case <-time.After(requestAttempts*snapshotRequestTimeout + time.Second):
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("state sync error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("state sync did not stop")
	}
}

func TestAuthenticationRejectionTemporary(t *testing.T) {
	for _, reason := range []string{
		"too many authentication attempts; retry later",
		"too many pending authentication handshakes",
		"authentication challenge expired",
		"authentication failed: server error",
		voice.ErrServerFull.Error(),
		voice.ErrAccountSessionLimit.Error(),
	} {
		if !(&AuthenticationRejectedError{Reason: reason}).Temporary() {
			t.Errorf("%q must allow a retry", reason)
		}
	}
	for _, reason := range []string{
		"server-password-required",
		"server-password-incorrect",
		"account is banned",
		voice.ErrDisplayNameTaken.Error(),
		"client upgrade required: minimum version 0.4.5",
		"too many new accounts from this network; retry later",
	} {
		if (&AuthenticationRejectedError{Reason: reason}).Temporary() {
			t.Errorf("%q must stop reconnecting", reason)
		}
	}
}
