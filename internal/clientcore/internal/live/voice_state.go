package client

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/logging"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/voiceflow"
)

type voiceStateEnvelope struct {
	generation uint64
	packet     protocol.VoicePacket
}

type installedVoiceState struct {
	header        protocol.VoiceStatePage
	sources       map[uint64]protocol.VoiceSourceState
	profile       domain.AudioProfile
	datagramFloor uint32
}

type VoicePacketSender interface {
	SendPacket(protocol.VoicePacket) error
}

func (s *State) clearVoiceStateLocked() {
	s.voice = nil
	s.voiceFlow = voiceflow.NewEndpoint(protocol.VoiceUpstream, time.Now())
	if s.voiceReserve > 0 {
		s.voiceFlow.SetBudget(s.voiceReserve)
	}
	s.voiceFrameEpoch, s.voiceFrameSequence = 0, 0
	s.ownControlRevision = 1
	s.lastVoiceCommandAck = nil
	s.voiceCommandInFlight = false
	s.voiceDeferredChannelCue = false
	select {
	case <-s.voiceResync:
	default:
	}
	for i := 0; i < cap(s.voicePages); i++ {
		select {
		case <-s.voicePages:
		default:
			return
		}
	}
}

func (s *State) VoiceState() (protocol.VoiceStatePage, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.voice == nil {
		return protocol.VoiceStatePage{}, false
	}
	return s.voice.header, true
}

// RequestVoiceResync is best effort and bounded; the actor rate limits requests.
func (s *State) RequestVoiceResync(generation uint64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.generation != generation {
		return
	}
	select {
	case s.voiceResync <- struct{}{}:
	default:
	}
}

// VoiceBundleAllowed is a policy gate, not a transport-loss measurement. The
// media runtime must count valid datagrams BEFORE invoking this gate, and check
// again before decode/playback when carrying frames through queues.
func (s *State) VoiceBundleAllowed(generation, senderID uint64, bundle protocol.VoiceBundleV2) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != generation || s.voice == nil || senderID == s.sessionID {
		return false
	}
	header := s.voice.header
	if bundle.ReceiverEpoch != header.OwnReceiverEpoch || bundle.SourceEpoch == 0 {
		return false
	}
	// Advance a bounded floor instead of comparing forever against one fixed
	// barrier: a long-lived receiver can cross 2^31 attempts and wrap uint32.
	distance := int32(bundle.DatagramSequence - s.voice.datagramFloor)
	if distance <= 0 {
		return false
	}
	if distance > 65536 {
		s.voice.datagramFloor = bundle.DatagramSequence - 65536
	}
	source, known := s.voice.sources[senderID]
	return header.Flags&protocol.VoiceStateOwnDeafened == 0 &&
		known && source.SourceEpoch == bundle.SourceEpoch && source.Flags&protocol.VoiceSourceMuted == 0
}

func (s *State) voiceProfileLocked(generation uint64, header protocol.VoiceStatePage) (domain.AudioProfile, bool) {
	if s.generation != generation || !s.snapshotFresh || s.syncedGeneration != generation ||
		uint64(s.snapshot.Revision) < header.AudioProfileRevision || uint64(s.channelID) != header.OwnChannelID {
		return domain.AudioProfile{}, false
	}
	for _, channel := range s.snapshot.Channels {
		if uint64(channel.ID) == header.OwnChannelID && domain.ValidateAudioProfile(channel.Audio) == nil {
			return channel.Audio, true
		}
	}
	return domain.AudioProfile{}, false
}

var errVoiceProfilePending = errors.New("voice profile is not synchronized")
var errVoiceStateSuperseded = errors.New("voice state superseded by command response")

// Lock order: Audio.sendMu -> State.mu -> Audio.mu. Encoder configuration is
// serialized with sends but never runs under State.mu. Local mute/deafen intent
// is NOT overwritten by possibly older server flags.
func (s *State) prepareVoiceStateLocked(snapshot *protocol.VoiceStateSnapshot, profile domain.AudioProfile) (*installedVoiceState, error) {
	header := snapshot.Header
	if header.OwnControlRevision == 0 {
		return nil, protocol.ErrInvalidVoiceState
	}
	if header.OwnControlRevision < s.ownControlRevision {
		return nil, errVoiceStateSuperseded
	}
	if ack := s.lastVoiceCommandAck; ack != nil && header.OwnControlRevision == ack.OwnControlRevision && !voiceHeaderMatchesAck(header, *ack) {
		return nil, protocol.ErrInvalidVoiceState
	}
	previous := s.voice
	if previous != nil && header.Revision <= previous.header.Revision {
		return nil, protocol.ErrInvalidVoiceState
	}
	sources := make(map[uint64]protocol.VoiceSourceState, len(snapshot.Sources))
	if header.Flags&protocol.VoiceStateFull == 0 {
		if previous == nil || header.BaseRevision != previous.header.Revision {
			return nil, protocol.ErrInvalidVoiceState
		}
		sources = maps.Clone(previous.sources)
	}
	for _, source := range snapshot.Sources {
		if source.ChannelID != header.OwnChannelID {
			return nil, protocol.ErrInvalidVoiceState
		}
		if previous != nil {
			if old, known := previous.sources[source.SenderID]; known && source.SourceEpoch < old.SourceEpoch {
				return nil, protocol.ErrInvalidVoiceState
			}
			if old, known := previous.sources[source.SenderID]; known && source.Flags&protocol.VoiceSourceRemoved == 0 && source.SourceEpoch == old.SourceEpoch && source.Flags != old.Flags {
				return nil, protocol.ErrInvalidVoiceState
			}
		}
		if source.Flags&protocol.VoiceSourceRemoved != 0 {
			delete(sources, source.SenderID)
		} else {
			sources[source.SenderID] = source
		}
	}
	own, known := sources[s.sessionID]
	if !known || own.SourceEpoch != header.OwnSourceEpoch ||
		(own.Flags&protocol.VoiceSourceMuted != 0) != (header.Flags&protocol.VoiceStateOwnMuted != 0) || len(sources) > protocol.MaxVoiceSources {
		return nil, protocol.ErrInvalidVoiceState
	}
	if previous != nil {
		old := previous.header
		if (header.OwnSourceEpoch == old.OwnSourceEpoch && header.Flags&protocol.VoiceStateOwnMuted != old.Flags&protocol.VoiceStateOwnMuted) ||
			(header.OwnReceiverEpoch == old.OwnReceiverEpoch && header.Flags&protocol.VoiceStateOwnDeafened != old.Flags&protocol.VoiceStateOwnDeafened) ||
			(header.OwnChannelID != old.OwnChannelID && (header.OwnSourceEpoch == old.OwnSourceEpoch || header.OwnReceiverEpoch == old.OwnReceiverEpoch)) {
			return nil, protocol.ErrInvalidVoiceState
		}
		if previous.profile != profile && header.OwnSourceEpoch == old.OwnSourceEpoch {
			return nil, errVoiceProfilePending
		}
		if header.OwnControlRevision < old.OwnControlRevision || header.OwnSourceEpoch < old.OwnSourceEpoch || header.OwnReceiverEpoch < old.OwnReceiverEpoch ||
			header.AudioProfileRevision < old.AudioProfileRevision ||
			(header.OwnReceiverEpoch == old.OwnReceiverEpoch && header.DownstreamBarrier != old.DownstreamBarrier) {
			return nil, protocol.ErrInvalidVoiceState
		}
		if header.OwnControlRevision == old.OwnControlRevision && (header.OwnSourceEpoch != old.OwnSourceEpoch ||
			header.OwnReceiverEpoch != old.OwnReceiverEpoch || header.OwnChannelID != old.OwnChannelID ||
			header.Flags&(protocol.VoiceStateOwnMuted|protocol.VoiceStateOwnDeafened) != old.Flags&(protocol.VoiceStateOwnMuted|protocol.VoiceStateOwnDeafened)) {
			return nil, protocol.ErrInvalidVoiceState
		}
	}
	header.Sources = nil
	return &installedVoiceState{header: header, sources: sources, profile: profile, datagramFloor: header.DownstreamBarrier}, nil
}

func (s *State) installVoiceState(generation uint64, snapshot *protocol.VoiceStateSnapshot, configure func(domain.AudioProfile) error) error {
	header := snapshot.Header
	s.Audio.sendMu.Lock()
	defer s.Audio.sendMu.Unlock()
	s.mu.Lock()
	profile, ready := s.voiceProfileLocked(generation, header)
	if !ready {
		s.mu.Unlock()
		return errVoiceProfilePending
	}
	previous := s.voice
	candidate, err := s.prepareVoiceStateLocked(snapshot, profile)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	s.Audio.mu.Lock()
	wasPaused := s.Audio.voicePaused
	configureNeeded := previous == nil || previous.header.OwnSourceEpoch != header.OwnSourceEpoch || previous.profile != profile || wasPaused
	if configureNeeded {
		s.Audio.voicePaused = true
	}
	s.Audio.mu.Unlock()
	if configure != nil && configureNeeded {
		if err := configure(profile); err != nil {
			return fmt.Errorf("configure voice state profile: %w", err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if current, ready := s.voiceProfileLocked(generation, header); !ready || current != profile || s.voice != previous {
		return errVoiceProfilePending
	}
	captureChanged := previous == nil || previous.header.OwnSourceEpoch != header.OwnSourceEpoch
	receiverChanged := previous == nil || previous.header.OwnReceiverEpoch != header.OwnReceiverEpoch
	s.Audio.mu.Lock()
	defer s.unlockVoiceAudioLocked()
	if receiverChanged && s.Audio.player != nil {
		if err := s.Audio.player.SetDeafened(true); err != nil {
			return err
		}
		if !s.Audio.deafened {
			if err := s.Audio.player.SetDeafened(s.Audio.commandPlaybackPaused); err != nil {
				return err
			}
		}
	}
	if captureChanged || configureNeeded {
		s.Audio.epoch++
		s.Audio.vadOpen = false
	}
	if receiverChanged {
		s.Audio.playbackEpoch++
		clear(s.speaking)
	}
	if !receiverChanged {
		candidate.datagramFloor = previous.datagramFloor
	}
	s.Audio.voicePaused = false
	s.voice = candidate
	s.voiceFlow.SetEnabled(header.EnabledDirections&protocol.VoiceDirectionUp != 0)
	s.ownControlRevision = header.OwnControlRevision
	if err := s.resumeVoiceCommandsLocked(); err != nil {
		return err
	}
	s.notifyLocked()
	return nil
}

// VoiceStateLoop is the sole owner of assembly and profile installation for a
// session. Subscription wakeups resume a snapshot waiting for ordinary state
// sync. Neither incomplete state nor an unavailable profile is acknowledged.
func VoiceStateLoop(ctx context.Context, conn VoicePacketSender, state *State, configure func(domain.AudioProfile) error) error {
	generation, sessionID := state.SessionIdentity()
	if sessionID == 0 {
		return ErrConnectionLost
	}
	changed, unsubscribe := state.Subscribe(ctx)
	defer unsubscribe()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	invalid := logging.NewFailures("voice_state_install")
	defer invalid.Close()
	var assembler protocol.VoiceStateAssembler
	var pending *protocol.VoiceStateSnapshot
	var lastRequest, lastResync time.Time
	needRequest := true
	send := func(kind uint8, payload []byte) error {
		if g, id := state.SessionIdentity(); g != generation || id != sessionID {
			return ErrConnectionLost
		}
		if err := conn.SendPacket(protocol.VoicePacket{Type: kind, SessionID: sessionID, Payload: payload}); err != nil {
			return fmt.Errorf("%w: voice state send: %w", ErrConnectionLost, err)
		}
		return nil
	}
	ack := func(revision uint64) error {
		payload, err := protocol.EncodeVoiceStateAck(revision)
		if err != nil {
			return err
		}
		return send(protocol.PacketVoiceStateAck, payload)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if g, id := state.SessionIdentity(); g != generation || id != sessionID {
			return ErrConnectionLost
		}
		now := time.Now()
		installed, haveInstalled := state.VoiceState()
		if assembler.Expire(now) {
			pending = nil
			needRequest = true
		}
		if pending != nil {
			err := state.installVoiceState(generation, pending, configure)
			if err == nil {
				installed, haveInstalled = pending.Header, true
				pending = nil
				assembler.Reset()
				needRequest = false
				if err := ack(installed.Revision); err != nil {
					return err
				}
			} else if errors.Is(err, errVoiceProfilePending) {
				if lastResync.IsZero() || now.Sub(lastResync) >= time.Second {
					state.RequestResync(generation)
					lastResync = now
				}
			} else if errors.Is(err, protocol.ErrInvalidVoiceState) || errors.Is(err, errVoiceStateSuperseded) {
				invalid.Record(err, sessionID)
				pending = nil
				assembler.Reset()
				needRequest = true
			} else {
				return err
			}
		}
		if !haveInstalled {
			needRequest = true
		}
		if needRequest && (lastRequest.IsZero() || now.Sub(lastRequest) >= time.Second) {
			if err := send(protocol.PacketVoiceStateRequest, protocol.EncodeVoiceStateRequest(installed.Revision)); err != nil {
				return err
			}
			lastRequest = now
			needRequest = false
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		case _, ok := <-changed:
			if !ok {
				return ctx.Err()
			}
		case <-state.voiceResync:
			needRequest = true
		case envelope := <-state.voicePages:
			if envelope.generation != generation || envelope.packet.SessionID != sessionID {
				continue
			}
			page, err := protocol.DecodeVoiceState(envelope.packet.Payload)
			if err != nil {
				invalid.Record(err, sessionID)
				needRequest = true
				continue
			}
			if pending != nil && page.Revision > pending.Header.Revision {
				pending = nil
			}
			snapshot, reack, err := assembler.Add(page, installed.Revision, time.Now())
			if err != nil {
				invalid.Record(err, sessionID)
				pending = nil
				assembler.Reset()
				needRequest = true
				continue
			}
			if reack {
				if err := ack(page.Revision); err != nil {
					return err
				}
			}
			if snapshot != nil {
				pending = snapshot
			}
		}
	}
}
