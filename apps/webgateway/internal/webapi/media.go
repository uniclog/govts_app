package webapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"uniclog.io/sonoryx/internal/clientcore"
	"uniclog.io/sonoryx/internal/mediasignal"
)

const maxMediaRequest = 256 << 10

func (h *handler) mediaSession(w http.ResponseWriter, r *http.Request) (*clientcore.App, uint64) {
	if h.cfg.MediaFingerprint == "" {
		fail(w, http.StatusNotFound, "На шлюзе отключена демонстрация экрана")
		return nil, 0
	}
	s := h.require(w, r)
	if s == nil {
		return nil, 0
	}
	return s.Core, s.Core.Snapshot().SessionID
}

func (h *handler) mediaCapabilities(w http.ResponseWriter, r *http.Request) {
	core, id := h.mediaSession(w, r)
	if core == nil {
		return
	}
	signaling, err := core.MediaSignaling(id)
	if err != nil {
		fail(w, http.StatusNotFound, "Сервер не предоставил демонстрацию экрана")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	caps, err := signaling.Capabilities(ctx, h.cfg.MediaFingerprint)
	mediaReply(w, caps, err)
}

func validMediaOffer(offer mediasignal.SessionDescription) bool {
	return offer.Type == "offer" && offer.SDP != ""
}

func validStreamID(value string) bool {
	id, err := strconv.ParseUint(value, 10, 64)
	return err == nil && id != 0 && decimal(id) == value
}

func (h *handler) mediaPublish(w http.ResponseWriter, r *http.Request) {
	if !h.origin(w, r) {
		return
	}
	core, id := h.mediaSession(w, r)
	if core == nil {
		return
	}
	var input mediasignal.PublishRequest
	if !readJSON(w, r, maxMediaRequest, &input) {
		return
	}
	if !validMediaOffer(input.Offer) {
		fail(w, http.StatusBadRequest, "Некорректное предложение WebRTC")
		return
	}
	runMedia(w, r, func(ctx context.Context) (any, error) {
		client, err := core.MediaSignaling(id)
		if err != nil {
			return nil, err
		}
		return client.Publish(ctx, input.Offer, h.cfg.MediaFingerprint)
	})
}

func (h *handler) mediaSubscribe(w http.ResponseWriter, r *http.Request) {
	if !h.origin(w, r) {
		return
	}
	core, id := h.mediaSession(w, r)
	if core == nil {
		return
	}
	var input mediasignal.SubscribeRequest
	if !readJSON(w, r, maxMediaRequest, &input) {
		return
	}
	if !validMediaOffer(input.Offer) || !validStreamID(input.StreamID) || !requestID.MatchString(input.SubscriberID) {
		fail(w, http.StatusBadRequest, "Некорректный запрос просмотра")
		return
	}
	runMedia(w, r, func(ctx context.Context) (any, error) {
		client, err := core.MediaSignaling(id)
		if err != nil {
			return nil, err
		}
		return client.Subscribe(ctx, input.StreamID, input.SubscriberID, input.Offer, h.cfg.MediaFingerprint)
	})
}

func (h *handler) mediaStop(w http.ResponseWriter, r *http.Request)        { h.endMedia(w, r, true) }
func (h *handler) mediaUnsubscribe(w http.ResponseWriter, r *http.Request) { h.endMedia(w, r, false) }

func (h *handler) endMedia(w http.ResponseWriter, r *http.Request, stop bool) {
	if !h.origin(w, r) {
		return
	}
	core, id := h.mediaSession(w, r)
	if core == nil {
		return
	}
	var input mediasignal.StreamRequest
	if !readJSON(w, r, 1024, &input) {
		return
	}
	if !validStreamID(input.StreamID) || (stop && input.SubscriberID != "") || (!stop && !requestID.MatchString(input.SubscriberID)) {
		fail(w, http.StatusBadRequest, "Некорректный идентификатор демонстрации")
		return
	}
	runMedia(w, r, func(ctx context.Context) (any, error) {
		if stop {
			return nil, core.StopScreen(ctx, id, input.StreamID, h.cfg.MediaFingerprint)
		}
		client, err := core.MediaSignaling(id)
		if err != nil {
			return nil, err
		}
		return nil, client.Unsubscribe(ctx, input.StreamID, input.SubscriberID, h.cfg.MediaFingerprint)
	})
}

func runMedia(w http.ResponseWriter, r *http.Request, call func(context.Context) (any, error)) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(22 * time.Second))
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	value, err := call(ctx)
	mediaReply(w, value, err)
}

func mediaReply(w http.ResponseWriter, value any, err error) {
	if err != nil {
		fail(w, http.StatusConflict, "Sonoryx отклонил запрос демонстрации экрана")
		return
	}
	if value == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if raw, ok := value.(json.RawMessage); ok && len(raw) == 0 {
		fail(w, http.StatusBadGateway, "Sonoryx вернул пустые параметры демонстрации")
		return
	}
	jsonReply(w, http.StatusOK, value)
}
