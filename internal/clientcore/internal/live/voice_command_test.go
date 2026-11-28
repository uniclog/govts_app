package client

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	voice "uniclog.io/sonoryx/apps/server/testsupport/voice"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

func preparedCommandPeer(t *testing.T) (*joinTestPeer, *protocol.VoiceStateSnapshot) {
	t.Helper()
	peer := newJoinTestPeer(t)
	peer.state.SetChannelID(1)
	peer.state.ReplaceSnapshot(domain.ServerSnapshot{Revision: 10, Channels: []domain.Channel{{ID: 1, Audio: domain.DefaultAudioProfile()}, {ID: 7, Audio: domain.DefaultAudioProfile()}}, Participants: []domain.Participant{{SessionID: 42, ChannelID: 1}}})
	_, p := voiceClientFixture()
	p.Sources = []protocol.VoiceSourceState{{SenderID: 42, SourceEpoch: 1, ChannelID: 1}}
	if err := peer.state.installVoiceState(peer.state.Generation(), p, nil); err != nil {
		t.Fatal(err)
	}
	return peer, p
}

func TestJoinStaleRetriesNewRequestAndWaitsForInstalledGeneration(t *testing.T) {
	peer, p := preparedCommandPeer(t)
	s := peer.state
	_, _, oldCapture := s.Audio.Snapshot()
	_, oldPlayback := s.Audio.PlaybackSnapshot()
	done := make(chan error, 1)
	go func() {
		var previousID uint32
		for i := 0; i < 2; i++ {
			request, addr, err := peer.receiveRequest()
			if err != nil {
				done <- err
				return
			}
			channel, expected, err := protocol.DecodeJoinChannelRequest(request.Payload)
			want := uint64(1)
			if i == 1 {
				want = 3
			}
			if err != nil || channel != 7 || expected != want || request.RequestID == previousID {
				done <- fmt.Errorf("unexpected request %+v expected=%d err=%v", request, expected, err)
				return
			}
			previousID = request.RequestID
			a := protocol.VoiceControlAck{Status: protocol.VoiceControlStale, OwnControlRevision: 3, VoiceRevision: 2, DomainRevision: 11, ChannelID: 1, SourceEpoch: 3, ReceiverEpoch: 3}
			if i == 1 {
				a.Status = protocol.VoiceControlOK
				a.OwnControlRevision = 4
				a.DomainRevision = 12
				a.ChannelID = 7
				a.SourceEpoch = 4
				a.ReceiverEpoch = 4
			}
			payload, err := protocol.EncodeVoiceControlAck(a)
			if err != nil {
				done <- err
				return
			}
			if err := peer.sendResponse(addr, protocol.VoicePacket{Type: protocol.PacketJoinChannelAck, SessionID: 42, RequestID: request.RequestID, Payload: payload}); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	if err := JoinChannel(peer.ctx, peer.clientConn, s, 7); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	written := false
	_, _, capture := s.Audio.Snapshot()
	if err := s.Audio.Send(capture, func() error { written = true; return nil }); err != nil || written {
		t.Fatal("command Ack resumed audio before VoiceState")
	}
	ordinary := s.Snapshot()
	ordinary.Revision = 12
	ordinary.Participants[0].ChannelID = 7
	s.ReplaceSnapshot(ordinary)
	p.Header.Revision = 3
	p.Header.OwnControlRevision = 4
	p.Header.OwnChannelID = 7
	p.Header.OwnSourceEpoch = 4
	p.Header.OwnReceiverEpoch = 4
	p.Header.AudioProfileRevision = 12
	p.Sources[0].ChannelID = 7
	p.Sources[0].SourceEpoch = 4
	if err := s.installVoiceState(s.Generation(), p, nil); err != nil {
		t.Fatal(err)
	}
	_, _, capture = s.Audio.Snapshot()
	if err := s.Audio.Send(capture, func() error { written = true; return nil }); err != nil || !written {
		t.Fatal("installed state did not resume capture")
	}
	written = false
	_ = s.Audio.Send(oldCapture, func() error { written = true; return nil })
	_ = s.Audio.Play(oldPlayback, func() error { written = true; return nil })
	if written {
		t.Fatal("old media epoch escaped transition")
	}
	_, playback := s.Audio.PlaybackSnapshot()
	select {
	case cue := <-s.NotificationSounds():
		if cue.Kind != NotificationChannelChanged || cue.PlaybackEpoch != playback {
			t.Fatal("move cue used obsolete playback epoch")
		}
	default:
		t.Fatal("move cue lost during media pause")
	}
}

func TestRejectedJoinResumesPreviousMedia(t *testing.T) {
	peer, _ := preparedCommandPeer(t)
	done := make(chan error, 1)
	go func() {
		p, addr, err := peer.receiveRequest()
		if err == nil {
			err = peer.sendResponse(addr, protocol.NewErrorPacket(42, p.RequestID, "permission denied"))
		}
		done <- err
	}()
	err := JoinChannel(peer.ctx, peer.clientConn, peer.state, 7)
	var rejection *ServerRequestError
	if !errors.As(err, &rejection) || errors.Is(err, ErrConnectionLost) {
		t.Fatalf("definite refusal lost session: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if peer.state.ChannelID() != 1 || peer.state.SessionID() != 42 {
		t.Fatal("refusal changed identity")
	}
	_, _, epoch := peer.state.Audio.Snapshot()
	written := false
	_ = peer.state.Audio.Send(epoch, func() error { written = true; return nil })
	if !written {
		t.Fatal("definite refusal left old channel muted")
	}
}

func TestUnknownJoinOutcomeInvalidatesSessionWithoutResumingAudio(t *testing.T) {
	peer, _ := preparedCommandPeer(t)
	err := joinChannelWithTimeout(peer.ctx, peer.clientConn, peer.state, 7, 5*time.Millisecond)
	if !errors.Is(err, ErrConnectionLost) || peer.state.SessionID() != 0 || peer.state.ConnectionStatus() != ConnectionReconnecting {
		t.Fatalf("unknown outcome: %v", err)
	}
	_, _, epoch := peer.state.Audio.Snapshot()
	written := false
	_ = peer.state.Audio.Send(epoch, func() error { written = true; return nil })
	if written {
		t.Fatal("capture resumed after unknown join outcome")
	}
}

func TestVoiceCommandMuteDoesNotPauseIncomingAudio(t *testing.T) {
	s, p := voiceClientFixture()
	if err := s.installVoiceState(s.Generation(), p, nil); err != nil {
		t.Fatal(err)
	}
	_, epoch := s.Audio.PlaybackSnapshot()
	capture, playback := s.voiceAudioChanges(true, false)
	if !capture || playback {
		t.Fatal("mute selected wrong media directions")
	}
	if err := s.beginVoiceCommand(s.Generation(), capture, playback); err != nil {
		t.Fatal(err)
	}
	written := false
	_ = s.Audio.Play(epoch, func() error { written = true; return nil })
	if !written {
		t.Fatal("mute interrupted incoming audio")
	}
	_ = s.endVoiceCommand(s.Generation())
}

func TestVoiceCommandGateCancellationAndOldAckCannotRollback(t *testing.T) {
	s, p := voiceClientFixture()
	p.Header.OwnControlRevision = 5
	if err := s.installVoiceState(s.Generation(), p, nil); err != nil {
		t.Fatal(err)
	}
	a := protocol.VoiceControlAck{OwnControlRevision: 2, DomainRevision: 2, ChannelID: 7, SourceEpoch: 2, ReceiverEpoch: 2}
	if current, err := s.acceptVoiceCommandAck(s.Generation(), 1, a); err != nil || current {
		t.Fatalf("old response current=%v err=%v", current, err)
	}
	if revision, _ := s.voiceCommandRevision(s.Generation()); revision != 5 {
		t.Fatal("revision rolled back")
	}
	unlock, err := s.lockVoiceCommand(context.Background(), s.Generation())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := s.lockVoiceCommand(ctx, s.Generation()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("gate cancellation: %v", err)
	}
}

func TestAudioCommandStaleRetryAndServerCacheIsIdempotent(t *testing.T) {
	peer, _ := preparedCommandPeer(t)
	h := voice.NewHub()
	h.Add(&voice.Session{ID: 42, ChannelID: 1, SourceEpoch: 1, ReceiverEpoch: 1, OwnControlRevision: 1, Addr: peer.clientConn.LocalAddr().(*net.UDPAddr)})
	if err := h.SetAudioState(42, false, true); err != nil {
		t.Fatal(err)
	}
	if err := peer.state.Audio.SetMuted(true); err != nil {
		t.Fatal(err)
	}
	cache := voice.NewRequestCache()
	done := make(chan error, 1)
	var last protocol.VoicePacket
	go func() {
		for i := 0; i < 2; i++ {
			request, addr, err := peer.receiveRequest()
			if err != nil {
				done <- err
				return
			}
			muted, deafened, expected, err := protocol.DecodeAudioState(request.Payload)
			if err != nil || !muted || deafened || expected != uint64(i+1) {
				done <- fmt.Errorf("unexpected audio command expected=%d err=%v", expected, err)
				return
			}
			if err := voice.HandleAudioStatePacket(peer.serverConn, h, cache, request, addr); err != nil {
				done <- err
				return
			}
			last = request
		}
		done <- nil
	}()
	if err := publishAudioState(peer.ctx, peer.clientConn, peer.state, true, false); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	before, _ := h.Get(42)
	if !before.Muted || before.Deafened || before.OwnControlRevision != 3 {
		t.Fatalf("audio state=%+v", before)
	}
	original, _ := cache.Get(42, last.RequestID)
	last.Payload, _ = protocol.EncodeAudioState(false, false, 3)
	if err := voice.HandleAudioStatePacket(peer.serverConn, h, cache, last, before.Addr); err != nil {
		t.Fatal(err)
	}
	after, _ := h.Get(42)
	repeated, _ := cache.Get(42, last.RequestID)
	if after.Muted != before.Muted || after.OwnControlRevision != before.OwnControlRevision || !bytes.Equal(original.Payload, repeated.Payload) {
		t.Fatal("duplicate RequestID mutated audio twice")
	}
}
