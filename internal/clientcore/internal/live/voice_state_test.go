package client

import (
	"context"
	"errors"
	"math"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	voice "uniclog.io/sonoryx/apps/server/testsupport/voice"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

type voiceSenderFunc func(protocol.VoicePacket) error

func (f voiceSenderFunc) SendPacket(p protocol.VoicePacket) error { return f(p) }

func voiceClientFixture() (*State, *protocol.VoiceStateSnapshot) {
	s := NewState(9, "listener")
	s.SetChannelID(1)
	s.ReplaceSnapshot(domain.ServerSnapshot{Revision: 10, Channels: []domain.Channel{{ID: 1, Audio: domain.DefaultAudioProfile()}},
		Participants: []domain.Participant{{SessionID: 9, ChannelID: 1}, {SessionID: 8, ChannelID: 1}}})
	return s, &protocol.VoiceStateSnapshot{Header: protocol.VoiceStatePage{
		Flags: protocol.VoiceStateFull, Revision: 1, OwnControlRevision: 1, OwnSourceEpoch: 1,
		OwnReceiverEpoch: 1, OwnChannelID: 1, AudioProfileRevision: 10, PageCount: 1,
	}, Sources: []protocol.VoiceSourceState{{SenderID: 9, SourceEpoch: 1, ChannelID: 1}, {SenderID: 8, SourceEpoch: 1, ChannelID: 1}}}
}

func cloneVoiceSnapshot(p *protocol.VoiceStateSnapshot) *protocol.VoiceStateSnapshot {
	copy := *p
	copy.Sources = append([]protocol.VoiceSourceState(nil), p.Sources...)
	return &copy
}

func voiceTestPacket(t *testing.T, s *State, p *protocol.VoiceStateSnapshot) protocol.VoicePacket {
	t.Helper()
	page := p.Header
	page.Sources = p.Sources
	payload, err := protocol.EncodeVoiceState(page)
	if err != nil {
		t.Fatal(err)
	}
	return protocol.VoicePacket{Type: protocol.PacketVoiceState, SessionID: s.SessionID(), Payload: payload}
}

func nextVoiceTestPacket(t *testing.T, ch <-chan protocol.VoicePacket, kind uint8) protocol.VoicePacket {
	t.Helper()
	select {
	case p := <-ch:
		if p.Type != kind {
			t.Fatalf("packet type %d, want %d", p.Type, kind)
		}
		return p
	case <-time.After(2 * time.Second):
		t.Fatal("voice state packet timeout")
	}
	return protocol.VoicePacket{}
}

func startVoiceActor(t *testing.T, s *State, configure func(domain.AudioProfile) error) (chan protocol.VoicePacket, chan protocol.VoicePacket) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	in, out := make(chan protocol.VoicePacket, 20), make(chan protocol.VoicePacket, 20)
	done := make(chan error, 2)
	go func() { done <- ControlLoop(ctx, s, in) }()
	go func() {
		done <- VoiceStateLoop(ctx, voiceSenderFunc(func(p protocol.VoicePacket) error { out <- p; return nil }), s, configure)
	}()
	t.Cleanup(func() {
		cancel()
		for i := 0; i < 2; i++ {
			select {
			case err := <-done:
				if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, ErrConnectionLost) {
					t.Error(err)
				}
			case <-time.After(2 * time.Second):
				t.Error("voice actor did not stop")
			}
		}
	})
	return in, out
}

func TestVoiceStateWaitsForProfileAndReacksWithoutReset(t *testing.T) {
	s, p := voiceClientFixture()
	p.Header.AudioProfileRevision = 11
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var configured atomic.Int32
	in, out := startVoiceActor(t, s, func(profile domain.AudioProfile) error {
		_ = s.Snapshot() // Configure must not run under State.mu.
		if profile.Bitrate != 32000 {
			return errors.New("wrong profile configured")
		}
		configured.Add(1)
		close(entered)
		<-release
		return nil
	})
	nextVoiceTestPacket(t, out, protocol.PacketVoiceStateRequest)
	in <- voiceTestPacket(t, s, p)
	select {
	case <-s.resync:
	case <-time.After(2 * time.Second):
		t.Fatal("missing profile did not request resync")
	}
	select {
	case p := <-out:
		if p.Type == protocol.PacketVoiceStateAck {
			t.Fatal("acked missing profile")
		}
	default:
	}
	ordinary := s.Snapshot()
	ordinary.Revision = 11
	ordinary.Channels[0].Audio.Bitrate = 32000
	s.ReplaceSnapshot(ordinary)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("profile not configured")
	}
	select {
	case p := <-out:
		if p.Type == protocol.PacketVoiceStateAck {
			t.Fatal("acked before configure completed")
		}
	default:
	}
	releaseOnce.Do(func() { close(release) })
	nextVoiceTestPacket(t, out, protocol.PacketVoiceStateAck)
	_, _, capture := s.Audio.Snapshot()
	_, playback := s.Audio.PlaybackSnapshot()
	in <- voiceTestPacket(t, s, p) // The first Ack may have been lost.
	nextVoiceTestPacket(t, out, protocol.PacketVoiceStateAck)
	_, _, afterCapture := s.Audio.Snapshot()
	_, afterPlayback := s.Audio.PlaybackSnapshot()
	if configured.Load() != 1 || capture != afterCapture || playback != afterPlayback {
		t.Fatal("duplicate reset audio")
	}
	peer := cloneVoiceSnapshot(p)
	peer.Header.Revision++
	peer.Sources[1].SourceEpoch++
	in <- voiceTestPacket(t, s, peer)
	nextVoiceTestPacket(t, out, protocol.PacketVoiceStateAck)
	_, _, afterCapture = s.Audio.Snapshot()
	_, afterPlayback = s.Audio.PlaybackSnapshot()
	if configured.Load() != 1 || capture != afterCapture || playback != afterPlayback {
		t.Fatal("peer reset own capture/playback")
	}
	packet := protocol.VoiceBundleV2{DatagramSequence: 1, SourceEpoch: 1, ReceiverEpoch: 1}
	if s.VoiceBundleAllowed(s.Generation(), 8, packet) {
		t.Fatal("old peer epoch allowed")
	}
	packet.SourceEpoch = 2
	if !s.VoiceBundleAllowed(s.Generation(), 8, packet) {
		t.Fatal("installed peer epoch blocked")
	}
}

func TestVoiceStateRejectsInvalidSnapshotBeforeSideEffects(t *testing.T) {
	for name, mutate := range map[string]func(*protocol.VoiceStateSnapshot){
		"missing self":  func(p *protocol.VoiceStateSnapshot) { p.Sources = p.Sources[1:] },
		"self epoch":    func(p *protocol.VoiceStateSnapshot) { p.Sources[0].SourceEpoch++ },
		"self mute":     func(p *protocol.VoiceStateSnapshot) { p.Header.Flags |= protocol.VoiceStateOwnMuted },
		"wrong channel": func(p *protocol.VoiceStateSnapshot) { p.Sources[1].ChannelID++ },
	} {
		t.Run(name, func(t *testing.T) {
			s, p := voiceClientFixture()
			mutate(p)
			configured := false
			err := s.installVoiceState(s.Generation(), p, func(domain.AudioProfile) error { configured = true; return nil })
			if !errors.Is(err, protocol.ErrInvalidVoiceState) || configured {
				t.Fatalf("invalid install: configured=%v err=%v", configured, err)
			}
			if _, ok := s.VoiceState(); ok {
				t.Fatal("invalid state installed")
			}
		})
	}
}

func TestVoiceStateDeltaBarrierWrapAndStaleState(t *testing.T) {
	s, p := voiceClientFixture()
	p.Header.DownstreamBarrier = math.MaxUint32 - 2
	if err := s.installVoiceState(s.Generation(), p, nil); err != nil {
		t.Fatal(err)
	}
	bundle := protocol.VoiceBundleV2{SourceEpoch: 1, ReceiverEpoch: 1, DatagramSequence: math.MaxUint32 - 2}
	if s.VoiceBundleAllowed(s.Generation(), 8, bundle) {
		t.Fatal("barrier packet allowed")
	}
	for _, seq := range []uint32{math.MaxUint32 - 1, math.MaxUint32, 0, 1} {
		bundle.DatagramSequence = seq
		if !s.VoiceBundleAllowed(s.Generation(), 8, bundle) {
			t.Fatalf("wrap seq %d blocked", seq)
		}
	}
	// Cross the signed half-range and full uint32 without keeping an ancient
	// fixed barrier. Each step is within the bounded transport observation range.
	for i := uint64(1); i <= 4096; i++ {
		bundle.DatagramSequence = uint32(i * (1 << 20))
		if !s.VoiceBundleAllowed(s.Generation(), 8, bundle) {
			t.Fatalf("long session blocked at %d", i)
		}
	}
	delta := cloneVoiceSnapshot(p)
	delta.Header.Revision = 2
	delta.Header.BaseRevision = 1
	delta.Header.Flags = 0
	delta.Sources = []protocol.VoiceSourceState{{SenderID: 8, SourceEpoch: 1, ChannelID: 1, Flags: protocol.VoiceSourceRemoved}}
	if err := s.installVoiceState(s.Generation(), delta, nil); err != nil {
		t.Fatal(err)
	}
	if s.VoiceBundleAllowed(s.Generation(), 8, bundle) {
		t.Fatal("removed source retained")
	}
	stale := cloneVoiceSnapshot(p)
	stale.Header.Revision = 3
	stale.Header.OwnControlRevision = 0
	if err := s.installVoiceState(s.Generation(), stale, nil); !errors.Is(err, protocol.ErrInvalidVoiceState) {
		t.Fatalf("rollback accepted: %v", err)
	}
}

func TestVoiceStateReceiverChangeInvalidatesBufferedAudioButPreservesLocalIntent(t *testing.T) {
	s, p := voiceClientFixture()
	if err := s.installVoiceState(s.Generation(), p, nil); err != nil {
		t.Fatal(err)
	}
	invalid := cloneVoiceSnapshot(p)
	invalid.Header.Revision = 2
	invalid.Header.OwnControlRevision = 2
	invalid.Header.Flags |= protocol.VoiceStateOwnDeafened
	if err := s.installVoiceState(s.Generation(), invalid, nil); !errors.Is(err, protocol.ErrInvalidVoiceState) {
		t.Fatalf("deafen changed without receiver epoch: %v", err)
	}
	if err := s.Audio.SetMuted(true); err != nil {
		t.Fatal(err)
	}
	_, _, capture := s.Audio.Snapshot()
	_, playback := s.Audio.PlaybackSnapshot()
	next := cloneVoiceSnapshot(p)
	next.Header.Revision = 2
	next.Header.OwnControlRevision = 2
	next.Header.OwnReceiverEpoch = 2
	next.Header.DownstreamBarrier = 10
	if err := s.installVoiceState(s.Generation(), next, nil); err != nil {
		t.Fatal(err)
	}
	muted, _, afterCapture := s.Audio.Snapshot()
	_, afterPlayback := s.Audio.PlaybackSnapshot()
	if !muted || capture != afterCapture || afterPlayback != playback+1 {
		t.Fatal("receiver change overwrote local intent or capture")
	}
	written := false
	if err := s.Audio.Play(playback, func() error { written = true; return nil }); err != nil || written {
		t.Fatal("old PCM played")
	}
	if s.VoiceBundleAllowed(s.Generation(), 8, protocol.VoiceBundleV2{SourceEpoch: 1, ReceiverEpoch: 1, DatagramSequence: 11}) {
		t.Fatal("old receiver accepted")
	}
	if !s.VoiceBundleAllowed(s.Generation(), 8, protocol.VoiceBundleV2{SourceEpoch: 1, ReceiverEpoch: 2, DatagramSequence: 11}) {
		t.Fatal("new receiver blocked")
	}
}

func TestVoiceStateConfigureFailureAndSessionChangeNeverInstall(t *testing.T) {
	s, p := voiceClientFixture()
	failure := errors.New("encoder unavailable")
	if err := s.installVoiceState(s.Generation(), p, func(domain.AudioProfile) error { return failure }); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	if _, ok := s.VoiceState(); ok {
		t.Fatal("configured failure installed state")
	}
	generation := s.Generation()
	if err := s.installVoiceState(generation, p, func(domain.AudioProfile) error { s.InvalidateSession(ConnectionReconnecting); return nil }); !errors.Is(err, errVoiceProfilePending) {
		t.Fatalf("session changed: %v", err)
	}
	if _, ok := s.VoiceState(); ok {
		t.Fatal("old session state installed")
	}
	if s.VoiceBundleAllowed(generation, 8, protocol.VoiceBundleV2{SourceEpoch: 1, ReceiverEpoch: 1, DatagramSequence: 1}) {
		t.Fatal("old session media accepted")
	}
}

func TestVoiceStateEncryptedUDPReack(t *testing.T) {
	peer := newJoinTestPeer(t)
	hub := voice.NewHub()
	hub.Add(&voice.Session{ID: 42, Name: "listener", ChannelID: 1, Addr: peer.clientConn.LocalAddr().(*net.UDPAddr)})
	hub.Add(&voice.Session{ID: 43, Name: "speaker", ChannelID: 1})
	peer.state.SetChannelID(1)
	peer.state.ReplaceSnapshot(hub.ClientSnapshot())
	done := make(chan error, 1)
	ctx, cancel := context.WithCancel(peer.ctx)
	go func() { done <- VoiceStateLoop(ctx, peer.clientConn, peer.state, nil) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("UDP voice actor leaked")
		}
	})
	request, addr, err := peer.receiveRequest()
	if err != nil || request.Type != protocol.PacketVoiceStateRequest {
		t.Fatalf("request: %+v %v", request, err)
	}
	if err := voice.HandleVoiceStateControl(hub, request, addr, time.Now()); err != nil {
		t.Fatal(err)
	}
	deliveries := hub.DueVoiceStates(time.Now())
	if len(deliveries) != 1 || len(deliveries[0].Packets) != 1 {
		t.Fatal("server snapshot not queued")
	}
	packet := deliveries[0].Packets[0]
	for i := 0; i < 2; i++ {
		if err := peer.serverConn.WritePacket(42, addr, packet); err != nil {
			t.Fatal(err)
		}
		ack, _, err := peer.receiveRequest()
		if err != nil || ack.Type != protocol.PacketVoiceStateAck {
			t.Fatalf("ack: %+v %v", ack, err)
		}
		if revision, err := protocol.DecodeVoiceStateAck(ack.Payload); err != nil || revision != 1 {
			t.Fatalf("ack revision: %d %v", revision, err)
		}
		if err := voice.HandleVoiceStateControl(hub, ack, addr, time.Now()); err != nil {
			t.Fatal(err)
		}
		if !hub.VoiceRelayReady(42, 43, 1, 1) {
			t.Fatal("ack did not authorize server stream")
		}
	}
}

func TestVoiceStateProfileRaceKeepsSendingPausedUntilReconfigured(t *testing.T) {
	s, p := voiceClientFixture()
	if err := s.installVoiceState(s.Generation(), p, nil); err != nil {
		t.Fatal(err)
	}
	ordinary := s.Snapshot()
	ordinary.Revision = 11
	ordinary.Channels[0].Audio.Bitrate = 32000
	s.ReplaceSnapshot(ordinary)
	oldPeer := cloneVoiceSnapshot(p)
	oldPeer.Header.Revision = 2
	if err := s.installVoiceState(s.Generation(), oldPeer, nil); !errors.Is(err, errVoiceProfilePending) {
		t.Fatalf("new profile with old source accepted: %v", err)
	}
	next := cloneVoiceSnapshot(p)
	next.Header.Revision = 3
	next.Header.OwnSourceEpoch = 2
	next.Header.OwnControlRevision = 2
	next.Header.AudioProfileRevision = 11
	next.Sources[0].SourceEpoch = 2
	err := s.installVoiceState(s.Generation(), next, func(domain.AudioProfile) error {
		newer := s.Snapshot()
		newer.Revision = 12
		newer.Channels[0].Audio.Bitrate = 48000
		s.ReplaceSnapshot(newer)
		return nil
	})
	if !errors.Is(err, errVoiceProfilePending) {
		t.Fatalf("profile race installed: %v", err)
	}
	_, _, epoch := s.Audio.Snapshot()
	written := false
	if err := s.Audio.Send(epoch, func() error { written = true; return nil }); err != nil || written {
		t.Fatal("sending resumed with uninstalled codec settings")
	}
	next.Header.Revision = 4
	next.Header.OwnSourceEpoch = 3
	next.Header.OwnControlRevision = 3
	next.Header.AudioProfileRevision = 12
	next.Sources[0].SourceEpoch = 3
	if err := s.installVoiceState(s.Generation(), next, func(profile domain.AudioProfile) error {
		if profile.Bitrate != 48000 {
			t.Fatal("wrong resumed profile")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	_, _, epoch = s.Audio.Snapshot()
	if err := s.Audio.Send(epoch, func() error { written = true; return nil }); err != nil || !written {
		t.Fatal("sending did not resume")
	}
}

func TestVoiceStateLoopInstallsOnlyCompletePages(t *testing.T) {
	s, p := voiceClientFixture()
	for i := 0; i < 15; i++ {
		p.Sources = append(p.Sources, protocol.VoiceSourceState{SenderID: uint64(100 + i), SourceEpoch: 1, ChannelID: 1})
	}
	pages, err := protocol.PaginateVoiceState(p.Header, p.Sources)
	if err != nil {
		t.Fatal(err)
	}
	var configured atomic.Int32
	in, out := startVoiceActor(t, s, func(domain.AudioProfile) error { configured.Add(1); return nil })
	nextVoiceTestPacket(t, out, protocol.PacketVoiceStateRequest)
	for _, page := range pages {
		payload, err := protocol.EncodeVoiceState(page)
		if err != nil {
			t.Fatal(err)
		}
		in <- protocol.VoicePacket{Type: protocol.PacketVoiceState, SessionID: s.SessionID(), Payload: payload}
	}
	nextVoiceTestPacket(t, out, protocol.PacketVoiceStateAck)
	if configured.Load() != 1 || !s.VoiceBundleAllowed(s.Generation(), 114, protocol.VoiceBundleV2{SourceEpoch: 1, ReceiverEpoch: 1, DatagramSequence: 1}) {
		t.Fatal("partial state installed")
	}
}

func TestVoiceStateQueueBackpressureDoesNotBlockHeartbeat(t *testing.T) {
	s, p := voiceClientFixture()
	for i := 0; i < cap(s.voicePages); i++ {
		s.voicePages <- voiceStateEnvelope{generation: s.Generation()}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	control := make(chan protocol.VoicePacket, 2)
	response := s.RegisterRequest(77)
	go func() { done <- ControlLoop(ctx, s, control) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("control loop did not stop")
		}
	})
	control <- voiceTestPacket(t, s, p)
	control <- protocol.VoicePacket{Type: protocol.PacketHeartbeatAck, SessionID: s.SessionID(), RequestID: 77}
	select {
	case ack := <-response:
		if ack.Type != protocol.PacketHeartbeatAck {
			t.Fatal("wrong response")
		}
	case <-time.After(time.Second):
		t.Fatal("full voice queue blocked heartbeat response")
	}
	select {
	case <-s.voiceResync:
	default:
		t.Fatal("dropped state page did not request resync")
	}
}
