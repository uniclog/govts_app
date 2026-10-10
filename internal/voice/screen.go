package voice

import (
	"errors"
	"sort"

	"uniclog.io/sonoryx/internal/domain"
)

// StartScreenShare publishes channel-visible metadata after the media path is
// ready. A participant may own one stream, while a channel may have many.
func (h *Hub) StartScreenShare(ownerID uint64, streamID domain.StreamID) (domain.ScreenStream, error) {
	if streamID == 0 {
		return domain.ScreenStream{}, errors.New("screen stream ID must not be zero")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	session, ok := h.sessions[ownerID]
	if !ok {
		return domain.ScreenStream{}, ErrSessionNotFound
	}
	if session.ChannelID == 0 {
		return domain.ScreenStream{}, ErrSessionNotInChannel
	}
	if _, ok := h.streamByOwner[ownerID]; ok {
		return domain.ScreenStream{}, ErrScreenStreamExists
	}
	if _, ok := h.screenStreams[streamID]; ok {
		return domain.ScreenStream{}, errors.New("screen stream ID already exists")
	}
	stream := domain.ScreenStream{ID: streamID, OwnerSessionID: ownerID, ChannelID: session.ChannelID}
	h.screenStreams[streamID] = stream
	h.streamByOwner[ownerID] = streamID
	h.revision++
	h.emitLocked(domain.StateEvent{Kind: domain.ScreenStreamStarted, ScreenStream: stream})
	return stream, nil
}

func (h *Hub) StopScreenShare(ownerID uint64, streamID domain.StreamID) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	stream, ok := h.screenStreams[streamID]
	if !ok {
		return ErrScreenStreamNotFound
	}
	if stream.OwnerSessionID != ownerID {
		return ErrScreenStreamForbidden
	}
	h.stopScreenShareLocked(stream)
	return nil
}

func (h *Hub) ScreenStream(id domain.StreamID) (domain.ScreenStream, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	stream, ok := h.screenStreams[id]
	return stream, ok
}

func (h *Hub) ScreenStreams() []domain.ScreenStream {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.screenStreamsSnapshotLocked()
}

func (h *Hub) CanSubscribeScreen(sessionID uint64, streamID domain.StreamID) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	session, ok := h.sessions[sessionID]
	stream, streamOK := h.screenStreams[streamID]
	return ok && streamOK && session.ChannelID != 0 && session.ChannelID == stream.ChannelID
}

func (h *Hub) stopScreenShareByOwnerLocked(ownerID uint64) {
	streamID, ok := h.streamByOwner[ownerID]
	if !ok {
		return
	}
	if stream, exists := h.screenStreams[streamID]; exists {
		h.stopScreenShareLocked(stream)
	}
}

func (h *Hub) stopScreenShareLocked(stream domain.ScreenStream) {
	delete(h.screenStreams, stream.ID)
	delete(h.streamByOwner, stream.OwnerSessionID)
	h.revision++
	h.emitLocked(domain.StateEvent{Kind: domain.ScreenStreamStopped, ScreenStream: domain.ScreenStream{ID: stream.ID}})
}

func (h *Hub) screenStreamsSnapshotLocked() []domain.ScreenStream {
	streams := make([]domain.ScreenStream, 0, len(h.screenStreams))
	for _, stream := range h.screenStreams {
		streams = append(streams, stream)
	}
	sort.Slice(streams, func(i, j int) bool { return streams[i].ID < streams[j].ID })
	return streams
}
