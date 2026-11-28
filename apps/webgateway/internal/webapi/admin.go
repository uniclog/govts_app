package webapi

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"uniclog.io/sonoryx/internal/clientcore"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

var administration = map[string]protocol.AdminOperation{
	"accounts": protocol.AdminAccounts, "permission": protocol.AdminSetPermission, "join-level": protocol.AdminSetJoinLevel,
	"ban": protocol.AdminSetBan, "audit": protocol.AdminAudit,
	"create-channel": protocol.AdminCreateChannel, "update-channel": protocol.AdminUpdateChannel,
	"delete-channel": protocol.AdminDeleteChannel, "delete-account": protocol.AdminDeleteAccount,
}

type channelSettings struct {
	ParentID     string `json:"parentId"`
	Position     uint32 `json:"position"`
	MaxUsers     uint32 `json:"maxUsers"`
	MinJoinLevel uint16 `json:"minJoinLevel"`
	Bitrate      uint32 `json:"bitrate"`
	Application  string `json:"application"`
	Name         string `json:"name"`
	Topic        string `json:"topic"`
	Description  string `json:"description"`
}

type adminAccount struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	JoinLevel   uint16 `json:"joinLevel"`
	Permissions uint8  `json:"permissions"`
	Owner       bool   `json:"owner"`
	Online      bool   `json:"online"`
	Banned      bool   `json:"banned"`
	Reason      string `json:"reason"`
}

type auditEvent struct {
	ID           string `json:"id"`
	At           int64  `json:"at"`
	Actor        string `json:"actor"`
	Action       string `json:"action"`
	TargetUserID string `json:"targetUserId"`
	Detail       string `json:"detail"`
}

func (h *handler) admin(w http.ResponseWriter, r *http.Request) {
	if !h.origin(w, r) {
		return
	}
	s := h.require(w, r)
	if s == nil {
		return
	}
	if s.Core.Snapshot().Permissions&(protocol.PrivilegeAdmin|protocol.PrivilegeOwner) == 0 {
		fail(w, http.StatusForbidden, "Нет прав администрирования")
		return
	}
	var input struct {
		Operation  string           `json:"operation"`
		Cursor     string           `json:"cursor"`
		ID         string           `json:"id"`
		Permission uint8            `json:"permission"`
		Enabled    bool             `json:"enabled"`
		Level      uint16           `json:"level"`
		Reason     string           `json:"reason"`
		Channel    *channelSettings `json:"channel"`
	}
	if !readJSON(w, r, 8192, &input) {
		return
	}
	operation, ok := administration[input.Operation]
	if !ok {
		fail(w, http.StatusBadRequest, "Неизвестная операция")
		return
	}
	request := protocol.AdminRequest{Operation: operation, Permission: input.Permission, Enabled: input.Enabled, Level: input.Level, Reason: input.Reason}
	var err error
	request.Cursor, err = parseAdminID(input.Cursor)
	if err == nil {
		request.TargetID, err = parseAdminID(input.ID)
	}
	if err == nil && input.Channel != nil {
		channel := input.Channel
		parent, parseErr := parseAdminID(channel.ParentID)
		err = parseErr
		app := domain.OpusApplicationVoIP
		if channel.Application == "audio" {
			app = domain.OpusApplicationAudio
		} else if channel.Application != "voip" {
			err = errCommand
		}
		request.Channel = protocol.AdminChannel{ParentID: parent, Name: channel.Name, Topic: channel.Topic,
			Description: channel.Description, Position: channel.Position, MaxUsers: channel.MaxUsers, MinJoinLevel: channel.MinJoinLevel,
			Bitrate: channel.Bitrate, Application: uint8(app)}
	}
	if err != nil {
		fail(w, http.StatusBadRequest, "Некорректные параметры")
		return
	}
	reply, err := s.Core.Administer(r.Context(), request)
	if err != nil {
		var serverErr *clientcore.ServerRequestError
		if errors.As(err, &serverErr) {
			fail(w, http.StatusConflict, serverErr.Message)
		} else {
			fail(w, http.StatusBadRequest, "Не удалось выполнить операцию")
		}
		return
	}
	result := struct {
		Accounts []adminAccount `json:"accounts,omitempty"`
		Audit    []auditEvent   `json:"audit,omitempty"`
		Next     string         `json:"next"`
	}{Next: decimal(reply.Next)}
	for _, account := range reply.Accounts {
		result.Accounts = append(result.Accounts, adminAccount{
			ID: decimal(account.ID), Name: account.Name, Fingerprint: hex.EncodeToString(account.Fingerprint[:]), JoinLevel: account.JoinLevel,
			Permissions: account.Permissions, Owner: account.Flags&protocol.AdminAccountOwner != 0,
			Online: account.Flags&protocol.AdminAccountOnline != 0, Banned: account.Flags&protocol.AdminAccountBanned != 0, Reason: account.BanReason,
		})
	}
	for _, event := range reply.Audit {
		result.Audit = append(result.Audit, auditEvent{ID: decimal(event.ID), At: event.HappenedAtMS, Actor: event.Actor,
			Action: event.Action, TargetUserID: decimal(event.TargetUserID), Detail: event.Detail})
	}
	jsonReply(w, http.StatusOK, result)
}

func parseAdminID(value string) (uint64, error) {
	if value == "" {
		return 0, nil
	}
	id, err := strconv.ParseUint(value, 10, 64)
	if err != nil || decimal(id) != value {
		return 0, errCommand
	}
	return id, nil
}

func (h *handler) moderate(w http.ResponseWriter, r *http.Request) {
	if !h.origin(w, r) {
		return
	}
	s := h.require(w, r)
	if s == nil {
		return
	}
	var input struct {
		Action        string `json:"action"`
		ParticipantID string `json:"participantId"`
		ChannelID     string `json:"channelId"`
	}
	if !readJSON(w, r, 1024, &input) {
		return
	}
	target, targetErr := parseAdminID(input.ParticipantID)
	channel, channelErr := parseAdminID(input.ChannelID)
	if targetErr != nil || channelErr != nil || target == 0 {
		fail(w, http.StatusBadRequest, "Некорректный участник или канал")
		return
	}
	var action, permission uint8
	switch input.Action {
	case "kick":
		action, permission = protocol.PacketKick, protocol.PrivilegeKick
	case "ban":
		action, permission = protocol.PacketBan, protocol.PrivilegeBan
	case "move":
		action, permission = protocol.PacketDrag, protocol.PrivilegeDrag
	}
	if action == 0 || (action == protocol.PacketDrag) != (channel != 0) {
		fail(w, http.StatusBadRequest, "Некорректное действие")
		return
	}
	if s.Core.Snapshot().Permissions&permission == 0 {
		fail(w, http.StatusForbidden, "У вас нет права на это действие")
		return
	}
	if err := s.Core.Moderate(r.Context(), action, target, domain.ChannelID(channel)); err != nil {
		fail(w, http.StatusConflict, "Sonoryx отклонил действие модератора")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) adminFiles(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && !h.origin(w, r) {
		return
	}
	s := h.require(w, r)
	if s == nil {
		return
	}
	if s.Core.Snapshot().Permissions&(protocol.PrivilegeAdmin|protocol.PrivilegeOwner) == 0 {
		fail(w, http.StatusForbidden, "Нет прав управления файлами")
		return
	}
	cursor := r.URL.Query().Get("cursor")
	if cursor != "" && !validAttachmentID(cursor) {
		fail(w, http.StatusBadRequest, "Некорректный курсор")
		return
	}
	client := s.Core.FileTransfers()
	if r.Method == http.MethodPost {
		var input struct {
			Action   string          `json:"action"`
			ID       string          `json:"id,omitempty"`
			Config   json.RawMessage `json:"config,omitempty"`
			Upload   *bool           `json:"upload,omitempty"`
			Download *bool           `json:"download,omitempty"`
		}
		if !readJSON(w, r, 8192, &input) {
			return
		}
		switch input.Action {
		case "policy":
			if input.ID != "" || len(input.Config) == 0 || input.Upload != nil || input.Download != nil {
				fail(w, http.StatusBadRequest, "Некорректная политика")
				return
			}
		case "delete", "rights":
			if !validAttachmentID(input.ID) || len(input.Config) != 0 ||
				(input.Action == "rights" && (input.Upload == nil || input.Download == nil)) ||
				(input.Action == "delete" && (input.Upload != nil || input.Download != nil)) {
				fail(w, http.StatusBadRequest, "Некорректный запрос файловой админки")
				return
			}
		default:
			fail(w, http.StatusBadRequest, "Неизвестная операция")
			return
		}
		if _, err := client.FileAdmin(r.Context(), fileContext(s.Core), http.MethodPost, "", input); err != nil {
			fail(w, http.StatusConflict, "Изменение отклонено. Обновите список и проверьте права")
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	result, err := client.FileAdmin(r.Context(), fileContext(s.Core), http.MethodGet, cursor, nil)
	if err != nil {
		fail(w, http.StatusNotFound, "Файловая админка недоступна")
		return
	}
	jsonReply(w, http.StatusOK, result)
}

func (h *handler) fileRights(w http.ResponseWriter, r *http.Request) {
	s := h.require(w, r)
	if s == nil {
		return
	}
	if s.Core.Snapshot().Permissions&(protocol.PrivilegeAdmin|protocol.PrivilegeOwner) == 0 {
		fail(w, http.StatusForbidden, "Нет прав управления файлами")
		return
	}
	id := r.PathValue("id")
	if !validAttachmentID(id) {
		http.NotFound(w, r)
		return
	}
	rights, err := s.Core.FileTransfers().FileAccountRights(r.Context(), fileContext(s.Core), id)
	if err != nil {
		fail(w, http.StatusNotFound, "Файловые права недоступны")
		return
	}
	jsonReply(w, http.StatusOK, rights)
}
