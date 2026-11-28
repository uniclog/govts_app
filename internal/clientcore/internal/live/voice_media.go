package client

import (
	"uniclog.io/sonoryx/internal/audio"
	"uniclog.io/sonoryx/internal/protocol"
)

type voiceMediaStamp struct{ generation, revision, channel uint64 }

func (s *State) mediaStamp() voiceMediaStamp {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stamp := voiceMediaStamp{generation: s.generation, channel: uint64(s.channelID)}
	if s.voice != nil {
		stamp.revision = s.voice.header.Revision
	}
	return stamp
}

func (s *State) reserveVoiceFrame(controlEpoch uint64) (audio.VoiceIdentity, uint32, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Audio.mu.Lock()
	defer s.Audio.mu.Unlock()
	if s.voice == nil || s.sessionID == 0 || s.voice.header.OwnControlRevision < s.ownControlRevision || s.Audio.muted || s.Audio.voicePaused || s.Audio.commandCapturePaused || controlEpoch != s.Audio.epoch || s.voice.header.Flags&protocol.VoiceStateOwnMuted != 0 || uint64(s.channelID) != s.voice.header.OwnChannelID {
		return audio.VoiceIdentity{}, 0, false
	}
	if s.voiceFrameEpoch != s.voice.header.OwnSourceEpoch {
		s.voiceFrameEpoch = s.voice.header.OwnSourceEpoch
		s.voiceFrameSequence = 0
	}
	s.voiceFrameSequence++
	return audio.VoiceIdentity{Generation: s.generation, SourceEpoch: s.voiceFrameEpoch}, s.voiceFrameSequence, true
}

func (s *State) outgoingVoiceAllowedLocked(v audio.VoiceIdentity) bool {
	return s.sessionID != 0 && v.Generation == s.generation && s.voice != nil && v.SourceEpoch == s.voice.header.OwnSourceEpoch && s.voice.header.OwnControlRevision >= s.ownControlRevision && uint64(s.channelID) == s.voice.header.OwnChannelID
}

func (s *State) voiceMediaAllowedLocked(sender uint64, v audio.VoiceIdentity) bool {
	if v.Generation == 0 {
		return true
	} // trusted local PCM/effects, never wire ingress
	if s.voice == nil || s.sessionID == 0 || v.Generation != s.generation || v.ReceiverEpoch != s.voice.header.OwnReceiverEpoch || s.voice.header.Flags&protocol.VoiceStateOwnDeafened != 0 || uint64(s.channelID) != s.voice.header.OwnChannelID {
		return false
	}
	source, ok := s.voice.sources[sender]
	return ok && sender != s.sessionID && source.SourceEpoch == v.SourceEpoch && source.Flags&protocol.VoiceSourceMuted == 0
}

func (s *State) VoiceMediaAllowed(sender uint64, v audio.VoiceIdentity) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.voiceMediaAllowedLocked(sender, v)
}

func (s *State) playVoicePCM(frame audio.PCMFrame, write func() error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, ref := range frame.Voices {
		if !s.voiceMediaAllowedLocked(ref.SenderID, ref.Identity) {
			return nil
		}
	}
	return s.Audio.Play(frame.PlaybackEpoch, write)
}
