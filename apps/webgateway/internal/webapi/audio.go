package webapi

import (
	"errors"
	"log"
	"net/http"
	"strconv"

	"uniclog.io/sonoryx/apps/webgateway/internal/session"
)

func (h *handler) getAudio(w http.ResponseWriter, r *http.Request) {
	s := h.require(w, r)
	if s == nil {
		return
	}
	jsonReply(w, http.StatusOK, s.AudioSettings())
}
func (h *handler) updateAudio(w http.ResponseWriter, r *http.Request) {
	if !h.origin(w, r) {
		return
	}
	s := h.require(w, r)
	if s == nil {
		return
	}
	var change session.AudioChange
	if !readJSON(w, r, 1024, &change) {
		return
	}
	if err := s.UpdateAudio(change); err != nil {
		audioUpdateError(w, err, "Не удалось применить настройку звука")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func audioUpdateError(w http.ResponseWriter, err error, invalid string) {
	switch {
	case errors.Is(err, session.ErrExpired):
		fail(w, http.StatusUnauthorized, session.ErrExpired.Error())
	case errors.Is(err, session.ErrInvalidAudioSetting):
		fail(w, http.StatusBadRequest, invalid)
	default:
		log.Printf("web gateway: save audio profile: %v", err)
		fail(w, http.StatusInternalServerError, "Не удалось сохранить настройки звука")
	}
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
		if change.Volume == nil {
			fail(w, http.StatusBadRequest, "Громкость участника недоступна")
			return
		}
		if err := s.UpdateParticipantVolume(id, *change.Volume); err != nil {
			audioUpdateError(w, err, "Громкость участника недоступна")
			return
		}
	}
	jsonReply(w, http.StatusOK, struct {
		Volume float32 `json:"volume"`
	}{Volume: s.ParticipantVolume(id)})
}
