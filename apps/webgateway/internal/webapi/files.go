package webapi

import (
	"context"
	"encoding/hex"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"uniclog.io/sonoryx/internal/clientcore"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/fileapi"
)

func fileContext(core *clientcore.App) string {
	return core.ChatContext() + "|" + signed(core.Snapshot().UserID)
}

func validAttachmentID(id string) bool {
	value, err := strconv.ParseInt(id, 10, 64)
	return err == nil && value > 0 && signed(value) == id
}

func (h *handler) attachment(w http.ResponseWriter, r *http.Request) {
	s := h.require(w, r)
	if s == nil {
		return
	}
	id := r.PathValue("id")
	if !validAttachmentID(id) {
		http.NotFound(w, r)
		return
	}
	expected := fileContext(s.Core)
	client := s.Core.FileTransfers()
	headers := make(map[string]string)
	if requestedRange := r.Header.Get("Range"); requestedRange != "" {
		if !strings.HasPrefix(requestedRange, "bytes=") || strings.Contains(requestedRange, ",") {
			fail(w, http.StatusRequestedRangeNotSatisfiable, "Допустим только один диапазон байтов")
			return
		}
		headers["Range"] = requestedRange
		if condition := r.Header.Get("If-Range"); condition != "" {
			headers["If-Range"] = condition
		}
	}
	response, release, err := client.Request(r.Context(), expected, http.MethodGet, "/files/v1/attachments/"+id+"/content", nil, headers)
	if err != nil {
		fail(w, http.StatusNotFound, "Вложение недоступно")
		return
	}
	defer release()
	_ = http.NewResponseController(w).SetWriteDeadline(s.Expires())
	for _, header := range []string{"Accept-Ranges", "Content-Disposition", "Content-Length", "Content-Range", "ETag", "Last-Modified"} {
		if value := response.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(response.StatusCode)
	if r.Method == http.MethodGet {
		if _, err := io.Copy(w, response.Body); err != nil {
			panic(http.ErrAbortHandler)
		}
	}
}

func (h *handler) attachmentInfo(w http.ResponseWriter, r *http.Request) {
	s := h.require(w, r)
	if s == nil {
		return
	}
	id := r.PathValue("id")
	if !validAttachmentID(id) {
		http.NotFound(w, r)
		return
	}
	items, err := s.Core.FileTransfers().FileMetadata(r.Context(), fileContext(s.Core), []string{id})
	if err != nil || len(items) != 1 || items[0].Deleted {
		fail(w, http.StatusNotFound, "Вложение недоступно")
		return
	}
	jsonReply(w, http.StatusOK, struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	}{Name: items[0].Name, Size: items[0].Size})
}

func (h *handler) attachmentPreview(w http.ResponseWriter, r *http.Request) {
	s := h.require(w, r)
	if s == nil {
		return
	}
	id := r.PathValue("id")
	if !validAttachmentID(id) {
		http.NotFound(w, r)
		return
	}
	data, err := s.Core.FileTransfers().FilePreview(r.Context(), fileContext(s.Core), id)
	if err != nil || len(data) == 0 {
		fail(w, http.StatusNotFound, "Эскиз недоступен")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = w.Write(data)
}

func (h *handler) fileCapabilities(w http.ResponseWriter, r *http.Request) {
	s := h.require(w, r)
	if s == nil {
		return
	}
	settings, err := s.Core.FileTransfers().FileCapabilities(r.Context(), fileContext(s.Core))
	if err != nil {
		fail(w, http.StatusNotFound, "Файлы недоступны")
		return
	}
	jsonReply(w, http.StatusOK, settings)
}

func (h *handler) startUpload(w http.ResponseWriter, r *http.Request) {
	if !h.origin(w, r) {
		return
	}
	s := h.require(w, r)
	if s == nil {
		return
	}
	var request struct {
		RequestID string `json:"requestId"`
		ChannelID string `json:"channelId"`
		UserID    string `json:"userId"`
		Name      string `json:"name"`
		Size      int64  `json:"size"`
		SHA256    string `json:"sha256"`
	}
	if !readJSON(w, r, 4096, &request) {
		return
	}
	kind, target := domain.ChatChannel, request.ChannelID
	if request.UserID != "" && request.ChannelID == "" {
		kind, target = domain.ChatDirect, request.UserID
	}
	id, err := strconv.ParseInt(target, 10, 64)
	if err != nil || id <= 0 || signed(id) != target || (request.UserID != "" && request.ChannelID != "") ||
		!hexID(request.RequestID, 16) || !hexID(request.SHA256, 32) || request.Name == "" || request.Size < 0 {
		fail(w, http.StatusBadRequest, "Некорректные параметры вложения")
		return
	}
	upload, err := s.Core.FileTransfers().StartUpload(r.Context(), fileContext(s.Core), fileapi.FileUpload{
		RequestID: request.RequestID, Kind: kind, TargetID: id, Name: request.Name, Size: request.Size, SHA256: request.SHA256,
	})
	if err != nil {
		fail(w, http.StatusConflict, "Sonoryx отклонил создание загрузки")
		return
	}
	jsonReply(w, http.StatusOK, upload)
}

func hexID(value string, size int) bool {
	if len(value) != 2*size {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func (h *handler) upload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead && !h.origin(w, r) {
		return
	}
	s := h.require(w, r)
	if s == nil {
		return
	}
	id := r.PathValue("upload")
	if !hexID(id, 16) {
		http.NotFound(w, r)
		return
	}
	client := s.Core.FileTransfers()
	expected := fileContext(s.Core)
	var state fileapi.FileUpload
	var err error
	switch r.Method {
	case http.MethodDelete:
		if err = client.CancelUpload(r.Context(), expected, id); err == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
	case http.MethodGet, http.MethodHead:
		state, err = client.UploadStatus(r.Context(), expected, id)
	case http.MethodPost:
		state, err = client.FinishUpload(r.Context(), expected, id)
	case http.MethodPatch:
		deadline := time.Now().Add(2 * time.Minute)
		controller := http.NewResponseController(w)
		_ = controller.SetReadDeadline(deadline)
		_ = controller.SetWriteDeadline(deadline)
		ctx, cancel := context.WithDeadline(r.Context(), deadline)
		defer cancel()
		rawOffset := r.Header.Get("Upload-Offset")
		offset, parseErr := strconv.ParseInt(rawOffset, 10, 64)
		if parseErr != nil || offset < 0 || signed(offset) != rawOffset {
			fail(w, http.StatusBadRequest, "Некорректное смещение загрузки")
			return
		}
		select {
		case h.fileChunks <- struct{}{}:
			defer func() { <-h.fileChunks }()
		default:
			fail(w, http.StatusTooManyRequests, "Слишком много загрузок, повторите позже")
			return
		}
		content, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, fileapi.ChunkSize))
		if readErr != nil || len(content) == 0 {
			fail(w, http.StatusBadRequest, "Некорректный фрагмент вложения")
			return
		}
		state, err = client.WriteChunk(ctx, expected, id, offset, content)
	}
	if err != nil {
		fail(w, http.StatusConflict, "Операция с файлом отклонена. Проверьте канал, размер и состояние загрузки")
		return
	}
	jsonReply(w, http.StatusOK, state)
}
