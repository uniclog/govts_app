package session

import (
	"encoding/json"
	"errors"
	"log"
	"maps"
	"os"
	"path/filepath"

	"uniclog.io/sonoryx/internal/clientcore"
	"uniclog.io/sonoryx/internal/fileops"
)

var ErrInvalidAudioSetting = errors.New("invalid audio setting")

type AudioSettings struct {
	Gain             float32 `json:"gain"`
	Noise            bool    `json:"noise"`
	NoiseSensitivity float32 `json:"noiseSensitivity"`
	VAD              bool    `json:"vad"`
	VADMode          string  `json:"vadMode"`
	VADSensitivity   float32 `json:"vadSensitivity"`
	SoundVolume      float32 `json:"soundVolume"`
	SoundPack        string  `json:"soundPack"`
	MuteOnAway       bool    `json:"muteOnAway"`
	DeafenOnAway     bool    `json:"deafenOnAway"`
}

type AudioChange struct {
	Name    string   `json:"name"`
	Number  *float32 `json:"number"`
	Enabled *bool    `json:"enabled"`
	Choice  string   `json:"choice"`
}

type audioProfile struct {
	Settings AudioSettings                          `json:"settings"`
	Volumes  map[string]map[int64]map[int64]float32 `json:"participantVolumes,omitempty"`
}

func (s *Session) AudioSettings() AudioSettings {
	s.audioMu.Lock()
	defer s.audioMu.Unlock()
	return audioSnapshot(s.Core)
}

func audioSnapshot(core *clientcore.App) AudioSettings {
	view := core.Snapshot()
	return AudioSettings{
		Gain: view.MicrophoneGain, Noise: view.RNNoiseEnabled, NoiseSensitivity: view.RNNoiseSensitivity,
		VAD: view.VADEnabled, VADMode: view.VADMode, VADSensitivity: view.VADSensitivity, SoundVolume: view.SoundVolume,
		SoundPack: view.SoundPack, MuteOnAway: view.MuteOnAway, DeafenOnAway: view.DeafenOnAway,
	}
}

func (settings AudioSettings) apply(core *clientcore.App) error {
	core.SetRNNoiseEnabled(settings.Noise)
	core.SetVADEnabled(settings.VAD)
	core.SetMuteOnAway(settings.MuteOnAway)
	core.SetDeafenOnAway(settings.DeafenOnAway)
	return errors.Join(
		core.SetMicrophoneGain(settings.Gain), core.SetRNNoiseSensitivity(settings.NoiseSensitivity),
		core.SetVADMode(settings.VADMode), core.SetVADSensitivity(settings.VADSensitivity),
		core.SetSoundVolume(settings.SoundVolume), core.SetSoundPack(settings.SoundPack),
	)
}

func (s *Session) loadAudioProfile() {
	data, err := os.ReadFile(s.audioPath)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	profile := audioProfile{Settings: audioSnapshot(s.Core)}
	if err == nil {
		err = json.Unmarshal(data, &profile)
	}
	if err != nil {
		log.Printf("web gateway: read audio profile: %v", err)
		return
	}
	if err := profile.Settings.apply(s.Core); err != nil {
		log.Printf("web gateway: restore audio settings: %v", err)
	}
	s.volumes = profile.Volumes
	s.Core.SetSavedParticipantVolumes(s.volumes[s.volumeScope])
}

func (s *Session) UpdateAudio(change AudioChange) error {
	s.audioMu.Lock()
	defer s.audioMu.Unlock()
	if s.ctx.Err() != nil {
		return ErrExpired
	}
	before := audioSnapshot(s.Core)
	if err := change.apply(s.Core); err != nil {
		return errors.Join(ErrInvalidAudioSetting, err)
	}
	if err := s.saveAudioProfile(audioProfile{Settings: audioSnapshot(s.Core), Volumes: s.volumes}); err != nil {
		return errors.Join(err, before.apply(s.Core))
	}
	return nil
}

func (s *Session) ParticipantVolume(id uint64) float32 {
	s.audioMu.Lock()
	defer s.audioMu.Unlock()
	return s.Core.ParticipantVolume(id)
}

func (s *Session) UpdateParticipantVolume(id uint64, volume float32) error {
	s.audioMu.Lock()
	defer s.audioMu.Unlock()
	if s.ctx.Err() != nil {
		return ErrExpired
	}
	view := s.Core.Snapshot()
	var remote int64
	for _, participant := range view.Participants {
		if participant.SessionID == id {
			remote = participant.UserID
			break
		}
	}
	if view.UserID <= 0 || remote <= 0 {
		return ErrInvalidAudioSetting
	}
	before := s.Core.ParticipantVolume(id)
	if err := s.Core.SetParticipantVolume(id, volume); err != nil {
		return errors.Join(ErrInvalidAudioSetting, err)
	}
	profile := audioProfile{Settings: audioSnapshot(s.Core), Volumes: maps.Clone(s.volumes)}
	if profile.Volumes == nil {
		profile.Volumes = make(map[string]map[int64]map[int64]float32)
	}
	byListener := maps.Clone(profile.Volumes[s.volumeScope])
	if byListener == nil {
		byListener = make(map[int64]map[int64]float32)
	}
	byParticipant := maps.Clone(byListener[view.UserID])
	if byParticipant == nil {
		byParticipant = make(map[int64]float32)
	}
	if volume == clientcore.DefaultParticipantVolume {
		delete(byParticipant, remote)
	} else {
		byParticipant[remote] = volume
	}
	if len(byParticipant) == 0 {
		delete(byListener, view.UserID)
	} else {
		byListener[view.UserID] = byParticipant
	}
	if len(byListener) == 0 {
		delete(profile.Volumes, s.volumeScope)
	} else {
		profile.Volumes[s.volumeScope] = byListener
	}
	if err := s.saveAudioProfile(profile); err != nil {
		_ = s.Core.SetParticipantVolume(id, before)
		return err
	}
	s.volumes = profile.Volumes
	s.Core.SetSavedParticipantVolumes(s.volumes[s.volumeScope])
	return nil
}

func (s *Session) saveAudioProfile(profile audioProfile) error {
	data, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.audioPath), ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(append(data, '\n'))
	if err == nil {
		err = tmp.Sync()
	}
	if err := errors.Join(err, tmp.Close()); err != nil {
		return err
	}
	return fileops.PublishFile(tmp.Name(), s.audioPath)
}

func (change AudioChange) apply(core *clientcore.App) error {
	switch {
	case change.Number != nil && change.Enabled == nil && change.Choice == "":
		switch change.Name {
		case "gain":
			return core.SetMicrophoneGain(*change.Number)
		case "noiseSensitivity":
			return core.SetRNNoiseSensitivity(*change.Number)
		case "vadSensitivity":
			return core.SetVADSensitivity(*change.Number)
		case "soundVolume":
			return core.SetSoundVolume(*change.Number)
		}
	case change.Enabled != nil && change.Number == nil && change.Choice == "":
		switch change.Name {
		case "noise":
			core.SetRNNoiseEnabled(*change.Enabled)
		case "vad":
			core.SetVADEnabled(*change.Enabled)
		case "muteOnAway":
			core.SetMuteOnAway(*change.Enabled)
		case "deafenOnAway":
			core.SetDeafenOnAway(*change.Enabled)
		default:
			return ErrInvalidAudioSetting
		}
		return nil
	case change.Choice != "" && change.Number == nil && change.Enabled == nil:
		switch change.Name {
		case "vadMode":
			return core.SetVADMode(change.Choice)
		case "soundPack":
			return core.SetSoundPack(change.Choice)
		}
	}
	return ErrInvalidAudioSetting
}
