package client

import (
	"context"
	"strings"
	"testing"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

func TestResolveChannelUsesIDAndRejectsAmbiguousName(t *testing.T) {
	snapshot := domain.ServerSnapshot{Channels: []domain.Channel{{ID: 2, Name: "room"}, {ID: 3, Name: "ROOM"}}}
	id, err := ResolveChannel(snapshot, "2")
	if err != nil || id != 2 {
		t.Fatalf("ID resolve = (%d, %v)", id, err)
	}
	if _, err := ResolveChannel(snapshot, "room"); err == nil {
		t.Fatal("ambiguous name accepted")
	}
}
func TestStateSnapshotUsesDeepCopies(t *testing.T) {
	state := NewState(7, "alice")
	original := domain.ServerSnapshot{Revision: 1, Info: domain.ServerInfo{Name: "s"}, Channels: []domain.Channel{{ID: 1, Name: "one"}}, Participants: []domain.Participant{{SessionID: 7, DisplayName: "alice", ChannelID: 1}}}
	state.ReplaceSnapshot(original)
	original.Channels[0].Name = "external"
	got := state.Snapshot()
	if got.Channels[0].Name != "one" {
		t.Fatal("ReplaceSnapshot retained caller slice")
	}
	got.Channels[0].Name = "changed"
	if state.Snapshot().Channels[0].Name != "one" {
		t.Fatal("Snapshot exposed internal slice")
	}
}

func TestLoadServerSnapshotRestartsAfterRevisionChange(t *testing.T) {
	peer := newJoinTestPeer(t)
	serverErr := make(chan error, 1)
	go func() {
		responses := []protocol.SnapshotResponse{
			{Kind: protocol.SnapshotKindMetadata, Status: protocol.SnapshotStatusOK, Revision: 2, ServerInfo: domain.ServerInfo{Name: "Server"}, ChannelCount: 1},
			{Kind: protocol.SnapshotKindChannels, Status: protocol.SnapshotStatusRevisionChanged, Revision: 3},
			{Kind: protocol.SnapshotKindMetadata, Status: protocol.SnapshotStatusOK, Revision: 3, ServerInfo: domain.ServerInfo{Name: "Server"}, ChannelCount: 1},
			{Kind: protocol.SnapshotKindChannels, Status: protocol.SnapshotStatusOK, Revision: 3, Channels: []domain.Channel{{ID: 1, Name: "default", Type: domain.ChannelTypePermanent, Audio: domain.DefaultAudioProfile()}}},
		}
		for _, response := range responses {
			request, addr, err := peer.receiveRequest()
			if err != nil {
				serverErr <- err
				return
			}
			payload, err := protocol.EncodeSnapshotResponse(response)
			if err != nil {
				serverErr <- err
				return
			}
			if err := peer.sendResponse(addr, protocol.VoicePacket{Type: protocol.PacketStateSnapshotAck, SessionID: request.SessionID, RequestID: request.RequestID, Payload: payload}); err != nil {
				serverErr <- err
				return
			}
		}
		serverErr <- nil
	}()
	snapshot, err := LoadServerSnapshot(peer.ctx, peer.clientConn, peer.state)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 3 || len(snapshot.Channels) != 1 || peer.state.Snapshot().Revision != 3 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestLoadServerSnapshotFailurePreservesPublishedState(t *testing.T) {
	peer := newJoinTestPeer(t)
	peer.state.ReplaceSnapshot(domain.ServerSnapshot{Revision: 1, Info: domain.ServerInfo{Name: "Old"}})
	serverErr := make(chan error, 1)
	go func() {
		request, addr, err := peer.receiveRequest()
		if err != nil {
			serverErr <- err
			return
		}
		serverErr <- peer.sendResponse(addr, protocol.VoicePacket{Type: protocol.PacketStateSnapshotAck, SessionID: request.SessionID, RequestID: request.RequestID, Payload: []byte{1, 2, 3}})
	}()
	if _, err := LoadServerSnapshot(context.Background(), peer.clientConn, peer.state); err == nil {
		t.Fatal("malformed snapshot response accepted")
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
	if got := peer.state.Snapshot(); got.Revision != 1 || got.Info.Name != "Old" {
		t.Fatalf("published state changed: %+v", got)
	}
}

func TestLoadServerSnapshotLimitsRevisionRestarts(t *testing.T) {
	peer := newJoinTestPeer(t)
	serverErr := make(chan error, 1)
	go func() {
		for revision := domain.StateRevision(1); revision <= snapshotSyncAttempts; revision++ {
			responses := []protocol.SnapshotResponse{
				{Kind: protocol.SnapshotKindMetadata, Status: protocol.SnapshotStatusOK, Revision: revision, ServerInfo: domain.ServerInfo{Name: "Server"}, ChannelCount: 1},
				{Kind: protocol.SnapshotKindChannels, Status: protocol.SnapshotStatusRevisionChanged, Revision: revision + 1},
			}
			for _, response := range responses {
				request, addr, err := peer.receiveRequest()
				if err != nil {
					serverErr <- err
					return
				}
				payload, err := protocol.EncodeSnapshotResponse(response)
				if err != nil {
					serverErr <- err
					return
				}
				if err := peer.sendResponse(addr, protocol.VoicePacket{Type: protocol.PacketStateSnapshotAck, SessionID: request.SessionID, RequestID: request.RequestID, Payload: payload}); err != nil {
					serverErr <- err
					return
				}
			}
		}
		serverErr <- nil
	}()
	if _, err := LoadServerSnapshot(peer.ctx, peer.clientConn, peer.state); err == nil || !strings.Contains(err.Error(), "3 snapshot attempts") {
		t.Fatalf("LoadServerSnapshot() error = %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}
