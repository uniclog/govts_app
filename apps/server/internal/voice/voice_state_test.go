package voice

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

func joinedVoiceSession(t *testing.T, h *Hub, name string) Session {
	t.Helper()
	s := mustCreateSession(t, h, name, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9000})
	if err := h.JoinChannel(s.ID, DefaultChannelID); err != nil {
		t.Fatal(err)
	}
	s, _ = h.Get(s.ID)
	return s
}

func onlyVoiceState(t *testing.T, h *Hub, now time.Time) protocol.VoiceStatePage {
	t.Helper()
	deliveries := h.DueVoiceStates(now)
	if len(deliveries) != 1 || len(deliveries[0].Packets) != 1 {
		t.Fatalf("deliveries = %+v", deliveries)
	}
	page, err := protocol.DecodeVoiceState(deliveries[0].Packets[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	return page
}

func TestVoiceStateRetryAckAndImmutablePages(t *testing.T) {
	h := NewHub()
	speaker := joinedVoiceSession(t, h, "speaker")
	listener := joinedVoiceSession(t, h, "listener")
	now := time.Unix(100, 0)
	if err := h.RequestVoiceState(listener.ID, now); err != nil {
		t.Fatal(err)
	}
	first := h.DueVoiceStates(now)
	if len(first) != 1 {
		t.Fatal("initial state not queued")
	}
	original := bytes.Clone(first[0].Packets[0].Payload)
	first[0].Packets[0].Payload[0] ^= 0xff
	if h.VoiceRelayReady(listener.ID, speaker.ID, speaker.SourceEpoch, listener.ReceiverEpoch) {
		t.Fatal("voice authorized before ack")
	}
	if h.AckVoiceState(listener.ID, 100) {
		t.Fatal("invented ack accepted")
	}
	if got := h.DueVoiceStates(now.Add(249 * time.Millisecond)); len(got) != 0 {
		t.Fatal("early retry")
	}
	for _, offset := range []time.Duration{250 * time.Millisecond, 750 * time.Millisecond, 1750 * time.Millisecond, 2750 * time.Millisecond} {
		delivery := h.DueVoiceStates(now.Add(offset))
		if len(delivery) != 1 || !bytes.Equal(delivery[0].Packets[0].Payload, original) {
			t.Fatalf("retry at %v changed or missing", offset)
		}
	}
	page, err := protocol.DecodeVoiceState(original)
	if err != nil {
		t.Fatal(err)
	}
	if !h.AckVoiceState(listener.ID, page.Revision) || !h.AckVoiceState(listener.ID, page.Revision) {
		t.Fatal("ack not idempotent")
	}
	if !h.VoiceRelayReady(listener.ID, speaker.ID, speaker.SourceEpoch, listener.ReceiverEpoch) {
		t.Fatal("acked stream blocked")
	}
	if len(h.DueVoiceStates(now.Add(10*time.Second))) != 0 {
		t.Fatal("acked snapshot keeps retrying")
	}
	if h.VoiceRelayReady(listener.ID, speaker.ID, 0, listener.ReceiverEpoch) {
		t.Fatal("zero source epoch accepted")
	}
}

func TestVoiceStateSourceChangeDoesNotBlockOtherSpeakers(t *testing.T) {
	h := NewHub()
	one, two, listener := joinedVoiceSession(t, h, "one"), joinedVoiceSession(t, h, "two"), joinedVoiceSession(t, h, "listener")
	now := time.Unix(100, 0)
	if err := h.RequestVoiceState(listener.ID, now); err != nil {
		t.Fatal(err)
	}
	initial := onlyVoiceState(t, h, now)
	if !h.AckVoiceState(listener.ID, initial.Revision) {
		t.Fatal("initial ack failed")
	}
	if err := h.SetAudioState(one.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if err := h.SetAudioState(one.ID, false, false); err != nil {
		t.Fatal(err)
	}
	oneNew, _ := h.Get(one.ID)
	listenerNew, _ := h.Get(listener.ID)
	if oneNew.SourceEpoch != one.SourceEpoch+2 || oneNew.ReceiverEpoch != one.ReceiverEpoch {
		t.Fatal("mute changed wrong generation")
	}
	if listenerNew.OwnControlRevision != listener.OwnControlRevision {
		t.Fatal("peer changed own control revision")
	}
	if h.VoiceRelayReady(listener.ID, one.ID, oneNew.SourceEpoch, listener.ReceiverEpoch) {
		t.Fatal("new source accepted before ack")
	}
	if !h.VoiceRelayReady(listener.ID, two.ID, two.SourceEpoch, listener.ReceiverEpoch) {
		t.Fatal("unchanged speaker blocked")
	}
	if !h.AckVoiceState(listener.ID, initial.Revision) {
		t.Fatal("duplicate old ack should be harmless")
	}
	if h.VoiceRelayReady(listener.ID, one.ID, oneNew.SourceEpoch, listener.ReceiverEpoch) {
		t.Fatal("old ack authorized new source")
	}
	latest := onlyVoiceState(t, h, now.Add(time.Second))
	if latest.Revision <= initial.Revision || !h.AckVoiceState(listener.ID, latest.Revision) {
		t.Fatal("coalesced state not installed")
	}
	if !h.VoiceRelayReady(listener.ID, one.ID, oneNew.SourceEpoch, listener.ReceiverEpoch) {
		t.Fatal("new source blocked after ack")
	}
}

func TestVoiceStateDeafenBarrierAndCounterSurviveTransition(t *testing.T) {
	h := NewHub()
	speaker, listener := joinedVoiceSession(t, h, "speaker"), joinedVoiceSession(t, h, "listener")
	now := time.Unix(100, 0)
	if err := h.RequestVoiceState(listener.ID, now); err != nil {
		t.Fatal(err)
	}
	first := onlyVoiceState(t, h, now)
	if !h.AckVoiceState(listener.ID, first.Revision) {
		t.Fatal("initial ack")
	}
	for want := uint32(1); want <= 3; want++ {
		if n, err := h.AllocateVoiceDatagram(listener.ID); err != nil || n != want {
			t.Fatalf("counter = %d %v", n, err)
		}
	}
	if err := h.SetAudioState(listener.ID, false, true); err != nil {
		t.Fatal(err)
	}
	page := onlyVoiceState(t, h, now.Add(time.Second))
	if page.DownstreamBarrier != 3 || page.OwnReceiverEpoch != listener.ReceiverEpoch+1 || page.OwnSourceEpoch != listener.SourceEpoch || page.Flags&protocol.VoiceStateOwnDeafened == 0 {
		t.Fatalf("deafen state = %+v", page)
	}
	if !h.AckVoiceState(listener.ID, page.Revision) {
		t.Fatal("deafen ack")
	}
	if h.VoiceRelayReady(listener.ID, speaker.ID, speaker.SourceEpoch, page.OwnReceiverEpoch) {
		t.Fatal("deafened listener authorized")
	}
	if err := h.SetAudioState(listener.ID, false, false); err != nil {
		t.Fatal(err)
	}
	page = onlyVoiceState(t, h, now.Add(2*time.Second))
	if page.Flags&protocol.VoiceStateOwnDeafened != 0 || page.DownstreamBarrier != 3 {
		t.Fatal("undeafen flags/barrier")
	}
	if !h.AckVoiceState(listener.ID, page.Revision) {
		t.Fatal("undeafen ack")
	}
	if !h.VoiceRelayReady(listener.ID, speaker.ID, speaker.SourceEpoch, page.OwnReceiverEpoch) {
		t.Fatal("undeafened stream blocked")
	}
	if n, err := h.AllocateVoiceDatagram(listener.ID); err != nil || n != 4 {
		t.Fatalf("transport counter reset: %d %v", n, err)
	}
	h.mu.Lock()
	h.sessions[listener.ID].voiceControl.lastDatagram = math.MaxUint32
	h.mu.Unlock()
	if n, err := h.AllocateVoiceDatagram(listener.ID); err != nil || n != 0 {
		t.Fatalf("counter wrap: %d %v", n, err)
	}
}

func TestVoiceStateChannelProfileAndDeletion(t *testing.T) {
	h := NewHub()
	speaker, listener := joinedVoiceSession(t, h, "speaker"), joinedVoiceSession(t, h, "listener")
	other, err := h.CreateChannel(domain.Channel{Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(100, 0)
	if err := h.RequestVoiceState(listener.ID, now); err != nil {
		t.Fatal(err)
	}
	page := onlyVoiceState(t, h, now)
	if !h.AckVoiceState(listener.ID, page.Revision) {
		t.Fatal("ack")
	}
	h.SetVoiceBundles(true)
	_, _ = h.SwapLastVoiceFrame(speaker.ID, protocol.VoiceBundleFrame{Sequence: 1, Payload: []byte{1}})
	channel, _ := h.GetChannel(DefaultChannelID)
	channel.Audio.Bitrate = 32000
	if _, err := h.UpdateChannel(channel); err != nil {
		t.Fatal(err)
	}
	changed, _ := h.Get(speaker.ID)
	if changed.SourceEpoch != speaker.SourceEpoch+1 || changed.ReceiverEpoch != speaker.ReceiverEpoch {
		t.Fatal("profile changed wrong generation")
	}
	if _, ok := h.SwapLastVoiceFrame(speaker.ID, protocol.VoiceBundleFrame{Sequence: 2, Payload: []byte{2}}); ok {
		t.Fatal("pre-profile audio retained")
	}
	page = onlyVoiceState(t, h, now.Add(time.Second))
	if page.AudioProfileRevision != uint64(h.Revision()) {
		t.Fatal("profile revision not updated")
	}
	before, _ := h.Get(listener.ID)
	channel.Topic = "metadata only"
	if _, err := h.UpdateChannel(channel); err != nil {
		t.Fatal(err)
	}
	after, _ := h.Get(listener.ID)
	if before.OwnControlRevision != after.OwnControlRevision || before.SourceEpoch != after.SourceEpoch {
		t.Fatal("metadata reset audio identity")
	}
	if err := h.JoinChannel(listener.ID, other.ID); err != nil {
		t.Fatal(err)
	}
	page = onlyVoiceState(t, h, now.Add(2*time.Second))
	if len(page.Sources) != 1 || page.Sources[0].SenderID != listener.ID || page.OwnChannelID != uint64(other.ID) {
		t.Fatal("old channel speaker retained")
	}
	if _, err := h.DeleteChannel(other.ID); err != nil {
		t.Fatal(err)
	}
	page = onlyVoiceState(t, h, now.Add(3*time.Second))
	if page.OwnChannelID != uint64(DefaultChannelID) || page.OwnSourceEpoch <= after.SourceEpoch || page.OwnReceiverEpoch <= after.ReceiverEpoch {
		t.Fatal("channel deletion did not rotate identity")
	}
}

func TestVoiceStateRequestGateAndExhaustion(t *testing.T) {
	h := NewHub()
	s := joinedVoiceSession(t, h, "one")
	now := time.Unix(100, 0)
	request := protocol.VoicePacket{Type: protocol.PacketVoiceStateRequest, SessionID: s.ID, Payload: protocol.EncodeVoiceStateRequest(0)}
	if err := HandleVoiceStateControl(h, request, s.Addr, now); err != nil {
		t.Fatal(err)
	}
	_ = onlyVoiceState(t, h, now)
	if err := HandleVoiceStateControl(h, request, s.Addr, now.Add(100*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if len(h.DueVoiceStates(now.Add(100*time.Millisecond))) != 0 {
		t.Fatal("request bypassed rate limit")
	}
	bad := request
	bad.RequestID = 1
	if err := HandleVoiceStateControl(h, bad, s.Addr, now); err == nil {
		t.Fatal("request ID accepted")
	}
	if err := HandleVoiceStateControl(h, request, &net.UDPAddr{IP: s.Addr.IP, Port: s.Addr.Port + 1}, now); !errors.Is(err, ErrInvalidSessionAddr) {
		t.Fatalf("wrong endpoint: %v", err)
	}
	copy, _ := h.Get(s.ID)
	if copy.voiceControl != nil {
		t.Fatal("mutable state leaked in Hub snapshot")
	}
	h.mu.Lock()
	h.sessions[s.ID].SourceEpoch = math.MaxUint32
	h.mu.Unlock()
	if err := h.SetAudioState(s.ID, true, false); !errors.Is(err, ErrVoiceEpochExhausted) {
		t.Fatalf("epoch exhaustion: %v", err)
	}
	if _, ok := h.Get(s.ID); ok {
		t.Fatal("exhausted session retained")
	}
}

func TestVoiceStateDispatcherWritesOutsideHubAndContinues(t *testing.T) {
	h := NewHub()
	one, two := joinedVoiceSession(t, h, "one"), joinedVoiceSession(t, h, "two")
	for _, id := range []uint64{one.ID, two.ID} {
		if err := h.RequestVoiceState(id, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	h.takeEvents()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	delivered := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- DispatchEvents(ctx, h, eventWriterFunc(func(id uint64, _ *net.UDPAddr, p protocol.VoicePacket) error {
			_ = h.Count()
			if p.Type != protocol.PacketVoiceState {
				return nil
			}
			if id == one.ID {
				return errors.New("unavailable recipient")
			}
			select {
			case delivered <- struct{}{}:
			default:
			}
			return nil
		}))
	}()
	select {
	case <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("voice state dispatcher blocked")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("dispatcher leaked")
	}
}

func TestVoiceControlRevisionIsAtomicAndIndependentFromPeers(t *testing.T) {
	h := NewHub()
	one, two := joinedVoiceSession(t, h, "one"), joinedVoiceSession(t, h, "two")
	now := time.Unix(100, 0)
	if err := h.RequestVoiceState(one.ID, now); err != nil {
		t.Fatal(err)
	}
	_ = onlyVoiceState(t, h, now)
	if err := h.SetAudioState(two.ID, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := h.SetAudioStateAtVoiceRevision(one.ID, true, false, one.OwnControlRevision); err != nil {
		t.Fatalf("peer invalidated own command: %v", err)
	}
	stale, err := h.SetAudioStateAtVoiceRevision(one.ID, false, false, one.OwnControlRevision)
	if !errors.Is(err, ErrStaleVoiceControl) || !stale.Session.Muted || stale.Session.OwnControlRevision != one.OwnControlRevision+1 {
		t.Fatalf("stale result = %+v %v", stale, err)
	}
	expected := stale.Session.OwnControlRevision
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, flags := range [][2]bool{{false, false}, {true, true}} {
		wg.Add(1)
		go func(flags [2]bool) {
			defer wg.Done()
			_, err := h.SetAudioStateAtVoiceRevision(one.ID, flags[0], flags[1], expected)
			results <- err
		}(flags)
	}
	wg.Wait()
	close(results)
	successes, stales := 0, 0
	for err := range results {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrStaleVoiceControl) {
			stales++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || stales != 1 {
		t.Fatalf("concurrent writes: successes=%d stales=%d", successes, stales)
	}
	current, _ := h.Get(one.ID)
	before := h.Revision()
	if _, err := h.JoinChannelAtVoiceRevision(one.ID, DefaultChannelID, current.OwnControlRevision); err != nil || h.Revision() != before {
		t.Fatalf("idempotent join mutated state: %v", err)
	}
}

func TestVoiceSourceLimitAlsoRejectsDeleteBeforePersistence(t *testing.T) {
	h := NewHub()
	other, err := h.CreateChannel(domain.Channel{Name: "overflow"})
	if err != nil {
		t.Fatal(err)
	}
	for id := uint64(1); id <= protocol.MaxVoiceSources; id++ {
		h.Add(&Session{ID: id, ChannelID: DefaultChannelID})
	}
	h.Add(&Session{ID: 5000, ChannelID: other.ID})
	h.Add(&Session{ID: 5001})
	revision := h.Revision()
	if _, err := h.CheckChannelDelete(other.ID); !errors.Is(err, ErrChannelFull) {
		t.Fatalf("pre-persistence delete check: %v", err)
	}
	if _, err := h.DeleteChannel(other.ID); !errors.Is(err, ErrChannelFull) {
		t.Fatalf("delete limit: %v", err)
	}
	if err := h.JoinChannel(5001, DefaultChannelID); !errors.Is(err, ErrChannelFull) {
		t.Fatalf("join limit: %v", err)
	}
	if h.Revision() != revision {
		t.Fatal("rejected operation mutated state")
	}
	if _, ok := h.GetChannel(other.ID); !ok {
		t.Fatal("rejected deletion removed channel")
	}
}

func TestVoiceProfileEpochExhaustionKeepsDomainEventRevisionsDistinct(t *testing.T) {
	h := NewHub()
	one := joinedVoiceSession(t, h, "one")
	_ = joinedVoiceSession(t, h, "two")
	h.takeEvents()
	h.mu.Lock()
	h.sessions[one.ID].SourceEpoch = math.MaxUint32
	h.mu.Unlock()
	channel, _ := h.GetChannel(DefaultChannelID)
	channel.Audio.Bitrate = 32000
	removed, err := h.UpdateChannel(channel)
	if err != nil || len(removed) != 1 || removed[0] != one.ID {
		t.Fatalf("retired IDs: %v %v", removed, err)
	}
	events := h.takeEvents()
	if len(events) != 2 || events[0].Kind != domain.ParticipantLeft || events[1].Kind != domain.ChannelUpdated || events[0].Revision >= events[1].Revision {
		t.Fatalf("non-monotonic domain events: %+v", events)
	}
}

func TestVoiceStatePeerDeafenDoesNotFanOutSnapshots(t *testing.T) {
	for _, acknowledged := range []bool{false, true} {
		t.Run(fmt.Sprint(acknowledged), func(t *testing.T) {
			h := NewHub()
			peer, listener := joinedVoiceSession(t, h, "peer"), joinedVoiceSession(t, h, "listener")
			now := time.Unix(100, 0)
			if err := h.RequestVoiceState(listener.ID, now); err != nil {
				t.Fatal(err)
			}
			first := h.DueVoiceStates(now)
			page, err := protocol.DecodeVoiceState(first[0].Packets[0].Payload)
			if err != nil {
				t.Fatal(err)
			}
			if acknowledged && !h.AckVoiceState(listener.ID, page.Revision) {
				t.Fatal("ack")
			}
			for i := 0; i < 50; i++ {
				if err := h.SetAudioState(peer.ID, false, i%2 == 0); err != nil {
					t.Fatal(err)
				}
			}
			if got := h.DueVoiceStates(now.Add(100 * time.Millisecond)); len(got) != 0 {
				t.Fatal("peer deafen triggered snapshot fanout")
			}
			if !acknowledged {
				retry := h.DueVoiceStates(now.Add(250 * time.Millisecond))
				if len(retry) != 1 || !bytes.Equal(retry[0].Packets[0].Payload, first[0].Packets[0].Payload) {
					t.Fatal("peer deafen replaced immutable pending snapshot")
				}
			}
		})
	}
}

func TestVoiceStateChannelMetadataDoesNotInvalidateAudio(t *testing.T) {
	h := NewHub()
	listener := joinedVoiceSession(t, h, "listener")
	now := time.Unix(100, 0)
	if err := h.RequestVoiceState(listener.ID, now); err != nil {
		t.Fatal(err)
	}
	page := onlyVoiceState(t, h, now)
	if !h.AckVoiceState(listener.ID, page.Revision) {
		t.Fatal("ack")
	}
	channel, _ := h.GetChannel(DefaultChannelID)
	channel.Topic = "metadata only"
	if _, err := h.UpdateChannel(channel); err != nil {
		t.Fatal(err)
	}
	if got := h.DueVoiceStates(now.Add(time.Second)); len(got) != 0 {
		t.Fatal("channel topic triggered voice snapshot")
	}
}

func TestVoiceStateResnapshotThenMutationDoesNotReuseAckedRevision(t *testing.T) {
	h := NewHub()
	s := joinedVoiceSession(t, h, "listener")
	now := time.Unix(100, 0)
	if err := h.RequestVoiceState(s.ID, now); err != nil {
		t.Fatal(err)
	}
	first := onlyVoiceState(t, h, now)
	if !h.AckVoiceState(s.ID, first.Revision) {
		t.Fatal("ack")
	}
	if err := h.RequestVoiceState(s.ID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := h.SetAudioState(s.ID, true, false); err != nil {
		t.Fatal(err)
	}
	next := onlyVoiceState(t, h, now.Add(time.Second))
	if next.Revision <= first.Revision || next.Flags&protocol.VoiceStateOwnMuted == 0 {
		t.Fatal("changed payload reused acknowledged revision")
	}
}
