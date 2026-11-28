package webapi

import (
	"net/http"
	"strconv"

	"uniclog.io/sonoryx/internal/clientcore"
)

type audioSettings struct {
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

func (h *handler) getAudio(w http.ResponseWriter, r *http.Request) {
	s := h.require(w, r)
	if s == nil {
		return
	}
	view := s.Core.Snapshot()
	jsonReply(w, http.StatusOK, audioSettings{
		Gain: view.MicrophoneGain, Noise: view.RNNoiseEnabled, NoiseSensitivity: view.RNNoiseSensitivity,
		VAD: view.VADEnabled, VADMode: view.VADMode, VADSensitivity: view.VADSensitivity, SoundVolume: view.SoundVolume,
		SoundPack: view.SoundPack, MuteOnAway: view.MuteOnAway, DeafenOnAway: view.DeafenOnAway,
	})
}
func (h *handler) updateAudio(w http.ResponseWriter, r *http.Request) {
	if !h.origin(w, r) {
		return
	}
	s := h.require(w, r)
	if s == nil {
		return
	}
	var change struct {
		Name    string   `json:"name"`
		Number  *float32 `json:"number"`
		Enabled *bool    `json:"enabled"`
		Choice  string   `json:"choice"`
	}
	if !readJSON(w, r, 1024, &change) {
		return
	}
	if err := applyAudio(s.Core, change.Name, change.Number, change.Enabled, change.Choice); err != nil {
		fail(w, http.StatusBadRequest, "Не удалось применить настройку звука")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func applyAudio(core *clientcore.App, name string, number *float32, enabled *bool, choice string) error {
	switch {
	case number != nil && enabled == nil && choice == "":
		switch name {
		case "gain":
			return core.SetMicrophoneGain(*number)
		case "noiseSensitivity":
			return core.SetRNNoiseSensitivity(*number)
		case "vadSensitivity":
			return core.SetVADSensitivity(*number)
		case "soundVolume":
			return core.SetSoundVolume(*number)
		}
	case enabled != nil && number == nil && choice == "":
		switch name {
		case "noise":
			core.SetRNNoiseEnabled(*enabled)
		case "vad":
			core.SetVADEnabled(*enabled)
		case "muteOnAway":
			core.SetMuteOnAway(*enabled)
		case "deafenOnAway":
			core.SetDeafenOnAway(*enabled)
		default:
			return errCommand
		}
		return nil
	case choice != "" && number == nil && enabled == nil:
		switch name {
		case "vadMode":
			return core.SetVADMode(choice)
		case "soundPack":
			return core.SetSoundPack(choice)
		}
	}
	return errCommand
}

func (h *handler) participantAudio(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && !h.origin(w, r) {
		return
	}
	s := h.require(w, r)
	if s == nil {
		return
	}
	id, err := strconv.ParseUint(r.PathValue("participant"), 10, 64)
	if err != nil || id == 0 || decimal(id) != r.PathValue("participant") {
		fail(w, http.StatusBadRequest, "Некорректный участник")
		return
	}
	if r.Method == http.MethodPost {
		var change struct {
			Volume *float32 `json:"volume"`
		}
		if !readJSON(w, r, 1024, &change) {
			return
		}
		if change.Volume == nil || s.Core.SetParticipantVolume(id, *change.Volume) != nil {
			fail(w, http.StatusBadRequest, "Громкость участника недоступна")
			return
		}
	}
	jsonReply(w, http.StatusOK, struct {
		Volume float32 `json:"volume"`
	}{Volume: s.Core.ParticipantVolume(id)})
}
