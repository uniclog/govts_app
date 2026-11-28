package client

import (
	"context"
	"errors"
	"fmt"

	"uniclog.io/sonoryx/internal/protocol"
)

var ErrVoiceControlStale = errors.New("voice control changed repeatedly; retry the operation")
var errVoiceIntentSuperseded = errors.New("local audio intent changed")

func (s *State) finishVoiceCommand(generation uint64, uncertain bool, err error) error {
	if uncertain && err != nil {
		s.mu.Lock()
		if s.generation == generation {
			s.invalidateSessionLocked(ConnectionReconnecting)
		}
		s.mu.Unlock()
		return fmt.Errorf("%w: voice command outcome is unknown: %w", ErrConnectionLost, err)
	}
	return errors.Join(err, s.endVoiceCommand(generation))
}

func (s *State) voiceAudioChanges(muted, deafened bool) (bool, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var oldMuted, oldDeafened bool
	if s.voice != nil {
		oldMuted = s.voice.header.Flags&protocol.VoiceStateOwnMuted != 0
		oldDeafened = s.voice.header.Flags&protocol.VoiceStateOwnDeafened != 0
	}
	if a := s.lastVoiceCommandAck; a != nil && a.OwnControlRevision == s.ownControlRevision {
		oldMuted, oldDeafened = a.Muted, a.Deafened
	}
	return muted != oldMuted, deafened != oldDeafened
}

func (s *State) lockVoiceCommand(ctx context.Context, generation uint64) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case s.voiceCommandGate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-s.voiceCommandGate
		return nil, err
	}
	if s.Generation() != generation {
		<-s.voiceCommandGate
		return nil, ErrConnectionLost
	}
	return func() { <-s.voiceCommandGate }, nil
}

func (s *State) beginVoiceCommand(generation uint64, capture, playback bool) error {
	s.Audio.sendMu.Lock()
	defer s.Audio.sendMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != generation {
		return ErrConnectionLost
	}
	s.Audio.mu.Lock()
	defer s.unlockVoiceAudioLocked()
	if playback && !s.Audio.commandPlaybackPaused && s.Audio.player != nil {
		if err := s.Audio.player.SetDeafened(true); err != nil {
			return err
		}
	}
	s.voiceCommandInFlight = true
	if capture && !s.Audio.commandCapturePaused {
		s.Audio.commandCapturePaused = true
		s.Audio.epoch++
		s.Audio.vadOpen = false
	}
	if playback && !s.Audio.commandPlaybackPaused {
		s.Audio.commandPlaybackPaused = true
		s.Audio.playbackEpoch++
	}
	return nil
}

func (s *State) endVoiceCommand(generation uint64) error {
	s.Audio.sendMu.Lock()
	defer s.Audio.sendMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != generation {
		return nil
	}
	s.Audio.mu.Lock()
	defer s.unlockVoiceAudioLocked()
	s.voiceCommandInFlight = false
	err := s.resumeVoiceCommandsLocked()
	s.notifyLocked()
	return err
}

// Caller holds State.mu and Audio.mu. Only a fully installed profile/state may
// release a command's media barrier, never the command Ack by itself.
func (s *State) resumeVoiceCommandsLocked() error {
	if s.voiceCommandInFlight || s.voice == nil || s.voice.header.OwnControlRevision < s.ownControlRevision {
		return nil
	}
	if s.Audio.commandPlaybackPaused && s.Audio.player != nil {
		if err := s.Audio.player.SetDeafened(s.Audio.deafened); err != nil {
			return err
		}
	}
	if s.Audio.commandCapturePaused {
		s.Audio.epoch++
	}
	s.Audio.commandCapturePaused = false
	s.Audio.commandPlaybackPaused = false
	return nil
}

// Called with State.mu held after a voice/audio transaction. Queue a move cue
// only after the new playback generation is installed, without Audio.mu held.
func (s *State) unlockVoiceAudioLocked() {
	playCue := s.voiceDeferredChannelCue && !s.Audio.commandPlaybackPaused && s.voice != nil && uint64(s.channelID) == s.voice.header.OwnChannelID
	if playCue {
		s.voiceDeferredChannelCue = false
	}
	s.Audio.mu.Unlock()
	if playCue {
		s.queueNotificationSoundLocked(NotificationChannelChanged)
	}
}

func voiceHeaderMatchesAck(h protocol.VoiceStatePage, a protocol.VoiceControlAck) bool {
	return h.OwnChannelID == uint64(a.ChannelID) && h.OwnSourceEpoch == a.SourceEpoch && h.OwnReceiverEpoch == a.ReceiverEpoch &&
		(h.Flags&protocol.VoiceStateOwnMuted != 0) == a.Muted && (h.Flags&protocol.VoiceStateOwnDeafened != 0) == a.Deafened
}

func (s *State) voiceCommandRevision(generation uint64) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.generation != generation {
		return 0, ErrConnectionLost
	}
	return s.ownControlRevision, nil
}

func (s *State) acceptVoiceCommandAck(generation, expected uint64, a protocol.VoiceControlAck) (bool, error) {
	if a.Status == protocol.VoiceControlStale {
		if a.OwnControlRevision <= expected {
			return false, protocol.ErrInvalidVoiceControlAck
		}
	} else if a.OwnControlRevision != expected && (expected == ^uint64(0) || a.OwnControlRevision != expected+1) {
		return false, protocol.ErrInvalidVoiceControlAck
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation != generation {
		return false, ErrConnectionLost
	}
	if a.OwnControlRevision < s.ownControlRevision {
		return false, nil
	}
	if s.voice != nil && a.OwnControlRevision == s.voice.header.OwnControlRevision && !voiceHeaderMatchesAck(s.voice.header, a) {
		return false, protocol.ErrInvalidVoiceControlAck
	}
	s.ownControlRevision = a.OwnControlRevision
	copy := a
	s.lastVoiceCommandAck = &copy
	s.notifyLocked()
	return true, nil
}
