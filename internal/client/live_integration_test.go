package client

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
	"uniclog.io/sonoryx/internal/voice"
)

type droppingEventWriter struct {
	conn *udp.ServerPacketConn
	drop atomic.Uint64
}

func (w *droppingEventWriter) WritePacket(id uint64, addr *net.UDPAddr, p protocol.VoicePacket) error {
	e, err := protocol.DecodeStateEvent(p.Payload)
	if err != nil {
		return err
	}
	if uint64(e.Revision) == w.drop.Load() {
		return nil
	}
	return w.conn.WritePacket(id, addr, p)
}

func TestLiveClientsAndLostLastEventRecovery(t *testing.T) {
	server := newSecureTestServer(t)
	hub := voice.NewHub()
	channel, err := hub.CreateChannel(domain.Channel{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	stopServer := server.serve(t, hub)
	defer stopServer()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w := &droppingEventWriter{conn: server.conn}
	dispatchDone := make(chan error, 1)
	go func() { dispatchDone <- voice.DispatchEvents(ctx, hub, w) }()
	defer func() {
		cancel()
		select {
		case err := <-dispatchDone:
			if !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("dispatcher leaked")
		}
	}()
	connect := func(name string) (*State, *udp.ClientPacketConn) {
		t.Helper()
		client := newSecureTestClient(t, server.addr())
		conn := client.conn
		id := client.connect(t, ctx, name)
		s := NewState(id, name)
		stop := startTestControlPipeline(t, conn, s)
		t.Cleanup(stop)
		if _, err := LoadServerSnapshot(ctx, conn, s); err != nil {
			t.Fatal(err)
		}
		return s, conn
	}
	alice, aliceConn := connect("alice")
	bob, bobConn := connect("bob")
	wait := func(f func() bool) {
		t.Helper()
		deadline := time.NewTimer(2 * time.Second)
		defer deadline.Stop()
		tick := time.NewTicker(5 * time.Millisecond)
		defer tick.Stop()
		for !f() {
			select {
			case <-deadline.C:
				t.Fatalf("state did not converge: %+v", alice.Snapshot())
			case <-tick.C:
			}
		}
	}
	wait(func() bool { return len(alice.Snapshot().Participants) == 2 })
	if err := JoinChannel(ctx, bobConn, bob, channel.ID); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		for _, p := range alice.Snapshot().Participants {
			if p.SessionID == bob.SessionID() {
				return p.ChannelID == channel.ID
			}
		}
		return false
	})
	// No following event: only the periodic metadata check can detect this loss.
	w.drop.Store(uint64(hub.Revision() + 1))
	if err := JoinChannel(ctx, bobConn, bob, voice.DefaultChannelID); err != nil {
		t.Fatal(err)
	}
	syncer := &StateSyncer{Conn: aliceConn, State: alice}
	syncDone := make(chan error, 1)
	syncCtx, stopSync := context.WithCancel(ctx)
	go func() { syncDone <- syncer.run(syncCtx, 20*time.Millisecond) }()
	defer func() {
		stopSync()
		select {
		case err := <-syncDone:
			if !errors.Is(err, context.Canceled) {
				t.Error(err)
			}
		case <-time.After(2 * time.Second):
			t.Error("syncer leaked")
		}
	}()
	wait(func() bool { return alice.Snapshot().Revision == hub.Revision() && alice.SnapshotFresh() })
	if err := Disconnect(bobConn, bob.SessionID()); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { return len(alice.Snapshot().Participants) == 1 })
	carol, _ := connect("carol")
	wait(func() bool { return len(alice.Snapshot().Participants) == 2 })
	carolSession, _ := hub.Get(carol.SessionID())
	// Windows clocks tick coarsely: alice must be seen strictly after carol,
	// or expiring carol expires alice too.
	for {
		if err := hub.Touch(alice.SessionID()); err != nil {
			t.Fatal(err)
		}
		if aliceSession, _ := hub.Get(alice.SessionID()); aliceSession.LastSeen.After(carolSession.LastSeen) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	hub.RemoveInactive(carolSession.LastSeen.Add(time.Minute), time.Minute)
	wait(func() bool { return len(alice.Snapshot().Participants) == 1 })
}
