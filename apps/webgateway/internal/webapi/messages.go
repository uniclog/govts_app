package webapi

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"uniclog.io/sonoryx/apps/webgateway/internal/session"
	"uniclog.io/sonoryx/internal/clientcore"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

type command struct {
	ID            string   `json:"id"`
	Type          string   `json:"type"`
	ChannelID     string   `json:"channelId,omitempty"`
	UserID        string   `json:"userId,omitempty"`
	ParticipantID string   `json:"participantId,omitempty"`
	Value         *bool    `json:"value,omitempty"`
	Forward       *bool    `json:"forward,omitempty"`
	Cursor        string   `json:"cursor,omitempty"`
	Text          string   `json:"text,omitempty"`
	ClientID      string   `json:"clientId,omitempty"`
	AttachmentIDs []string `json:"attachmentIds,omitempty"`
}
type result struct {
	Type  string    `json:"type"`
	ID    string    `json:"id"`
	OK    bool      `json:"ok"`
	Error string    `json:"error,omitempty"`
	Page  *chatPage `json:"page,omitempty"`
}
type channelView struct {
	ID      string `json:"id"`
	CanJoin bool   `json:"canJoin"`
	channelSettings
}
type participantView struct {
	ID        string `json:"id"`
	UserID    string `json:"userId"`
	ChannelID string `json:"channelId"`
	Name      string `json:"name"`
	Muted     bool   `json:"muted"`
	Deafened  bool   `json:"deafened"`
	Speaking  bool   `json:"speaking"`
	Away      bool   `json:"away"`
}
type snapshot struct {
	Type          string             `json:"type"`
	Status        string             `json:"status"`
	Error         string             `json:"error,omitempty"`
	ServerName    string             `json:"serverName"`
	Revision      string             `json:"revision"`
	ChatRevision  string             `json:"chatRevision"`
	SessionID     string             `json:"sessionId"`
	UserID        string             `json:"userId"`
	ChannelID     string             `json:"channelId"`
	Muted         bool               `json:"muted"`
	Deafened      bool               `json:"deafened"`
	CanAdmin      bool               `json:"canAdmin"`
	IsOwner       bool               `json:"isOwner"`
	CanKick       bool               `json:"canKick"`
	CanBan        bool               `json:"canBan"`
	CanDrag       bool               `json:"canDrag"`
	Channels      []channelView      `json:"channels"`
	Participants  []participantView  `json:"participants"`
	ScreenStreams []screenStreamView `json:"screenStreams"`
}
type screenStreamView struct {
	ID        string `json:"id"`
	OwnerID   string `json:"ownerId"`
	ChannelID string `json:"channelId"`
}

func decimal[T ~uint64](id T) string { return strconv.FormatUint(uint64(id), 10) }
func signed(id int64) string         { return strconv.FormatInt(id, 10) }

func state(s *session.Session, kind string) snapshot {
	view := s.Core.Snapshot()
	status := "disconnected"
	switch view.ConnectionStatus {
	case clientcore.ConnectionConnected:
		status = "connected"
	case clientcore.ConnectionConnecting:
		status = "connecting"
	case clientcore.ConnectionReconnecting:
		status = "reconnecting"
	}
	result := snapshot{Type: kind, Status: status, Error: s.ConnectionError(), ServerName: view.ServerInfo.Name, Revision: decimal(view.Revision), ChatRevision: decimal(view.ChatRevision), SessionID: decimal(view.SessionID), UserID: signed(view.UserID), ChannelID: decimal(view.ChannelID), Muted: view.Muted, Deafened: view.Deafened, Channels: make([]channelView, 0, len(view.Channels)), Participants: make([]participantView, 0, len(view.Participants))}
	result.CanAdmin = view.Permissions&(protocol.PrivilegeAdmin|protocol.PrivilegeOwner) != 0
	result.IsOwner = view.Permissions&protocol.PrivilegeOwner != 0
	result.CanKick = view.Permissions&protocol.PrivilegeKick != 0
	result.CanBan = view.Permissions&protocol.PrivilegeBan != 0
	result.CanDrag = view.Permissions&protocol.PrivilegeDrag != 0
	for _, c := range view.Channels {
		var minJoinLevel uint16
		if result.CanAdmin {
			minJoinLevel = c.MinJoinLevel
		}
		application := "voip"
		if c.Audio.Application == domain.OpusApplicationAudio {
			application = "audio"
		}
		result.Channels = append(result.Channels, channelView{ID: decimal(c.ID), CanJoin: view.JoinLevel >= c.MinJoinLevel, channelSettings: channelSettings{
			ParentID: decimal(c.ParentID), Name: c.Name, Topic: c.Topic, Description: c.Description, Position: c.Position,
			MaxUsers: c.MaxUsers, MinJoinLevel: minJoinLevel, Bitrate: c.Audio.Bitrate, Application: application,
		}})
	}
	for _, p := range view.Participants {
		result.Participants = append(result.Participants, participantView{ID: decimal(p.SessionID), UserID: signed(p.UserID), ChannelID: decimal(p.ChannelID), Name: p.DisplayName, Muted: p.Muted, Deafened: p.Deafened, Speaking: view.Speaking[p.SessionID], Away: p.Away})
	}
	result.ScreenStreams = make([]screenStreamView, 0, len(view.ScreenStreams))
	for _, stream := range view.ScreenStreams {
		result.ScreenStreams = append(result.ScreenStreams, screenStreamView{ID: decimal(stream.ID), OwnerID: decimal(stream.OwnerSessionID), ChannelID: decimal(stream.ChannelID)})
	}
	return result
}

type chatMessage struct {
	ID          string         `json:"id"`
	SenderID    string         `json:"senderId"`
	SenderName  string         `json:"senderName"`
	Text        string         `json:"text"`
	At          int64          `json:"sentAtMs"`
	Deleted     bool           `json:"deleted"`
	Revision    string         `json:"revision"`
	Reactions   []reactionView `json:"reactions,omitempty"`
	Attachments []string       `json:"attachmentIds,omitempty"`
}
type reactionView struct {
	Emoji string `json:"emoji"`
	Count uint16 `json:"count"`
	Mine  bool   `json:"mine"`
}
type dialogView struct {
	UserID string `json:"userId"`
	Name   string `json:"name"`
	Unread uint32 `json:"unread"`
}
type chatPage struct {
	ChannelID string        `json:"channelId,omitempty"`
	UserID    string        `json:"userId,omitempty"`
	Cursor    string        `json:"cursor"`
	HasMore   bool          `json:"hasMore"`
	Messages  []chatMessage `json:"messages"`
	Dialogs   []dialogView  `json:"dialogs,omitempty"`
	PinnedIDs []string      `json:"pinnedIds,omitempty"`
}

var errCommand = errors.New("Некорректная команда")
var errChatChannel = errors.New("Канальный чат доступен после входа в этот канал")

func execute(ctx context.Context, s *session.Session, c command) (*chatPage, error) {
	if c.Type != "chat.send" && len(c.AttachmentIDs) != 0 {
		return nil, errCommand
	}
	if c.Type != "wake" && c.ParticipantID != "" {
		return nil, errCommand
	}
	switch c.Type {
	case "join":
		id, err := strconv.ParseUint(c.ChannelID, 10, 64)
		if err != nil || id == 0 || decimal(id) != c.ChannelID || c.UserID != "" || c.Value != nil || c.Forward != nil || c.Cursor != "" || c.Text != "" || c.ClientID != "" {
			return nil, errCommand
		}
		return nil, s.Join(ctx, domain.ChannelID(id))
	case "mute", "deafen", "away":
		if c.Value == nil || c.Forward != nil || c.ChannelID != "" || c.UserID != "" || c.Cursor != "" || c.Text != "" || c.ClientID != "" {
			return nil, errCommand
		}
		switch c.Type {
		case "mute":
			return nil, s.Mute(*c.Value)
		case "away":
			return nil, s.Core.SetAway(ctx, *c.Value)
		default:
			return nil, s.Deafen(*c.Value)
		}
	case "wake":
		id, err := strconv.ParseUint(c.ParticipantID, 10, 64)
		if err != nil || id == 0 || decimal(id) != c.ParticipantID || c.ChannelID != "" || c.UserID != "" || c.Value != nil || c.Forward != nil || c.Cursor != "" || c.Text != "" || c.ClientID != "" {
			return nil, errCommand
		}
		return nil, s.Core.Wake(ctx, id)
	case "chat.history", "chat.watermark", "chat.changes", "chat.send", "chat.dialogs", "chat.read", "chat.delete", "chat.react", "chat.pin", "chat.pins":
		return chat(ctx, s, c)
	default:
		return nil, errCommand
	}
}

func chat(ctx context.Context, s *session.Session, c command) (*chatPage, error) {
	usesForward := c.Type == "chat.history" || c.Type == "chat.react" || c.Type == "chat.pin" || c.Type == "chat.pins"
	if c.Value != nil || (c.Type != "chat.send" && c.ClientID != "") ||
		(c.Type != "chat.send" && c.Type != "chat.react" && c.Text != "") ||
		(!usesForward && c.Forward != nil) ||
		((c.Type == "chat.send" || c.Type == "chat.watermark") && c.Cursor != "") {
		return nil, errCommand
	}
	view := s.Core.Snapshot()
	r := protocol.ChatRequest{Forward: c.Forward != nil && *c.Forward}
	switch {
	case c.Type == "chat.dialogs":
		if c.UserID != "" || c.ChannelID != "" {
			return nil, errCommand
		}
	case c.UserID != "":
		id, err := strconv.ParseInt(c.UserID, 10, 64)
		if c.ChannelID != "" || err != nil || id <= 0 || id == view.UserID || signed(id) != c.UserID {
			return nil, errCommand
		}
		r.Target = domain.ChatTarget{Kind: domain.ChatDirect, ID: id}
	default:
		if view.ChannelID == 0 || decimal(view.ChannelID) != c.ChannelID {
			return nil, errChatChannel
		}
		id, err := strconv.ParseInt(c.ChannelID, 10, 64)
		if err != nil || id <= 0 {
			return nil, errChatChannel
		}
		r.Target = domain.ChatTarget{Kind: domain.ChatChannel, ID: id}
	}
	if c.Cursor != "" {
		var err error
		r.Cursor, err = strconv.ParseInt(c.Cursor, 10, 64)
		if err != nil || r.Cursor < 0 || signed(r.Cursor) != c.Cursor {
			return nil, errCommand
		}
	}
	switch c.Type {
	case "chat.history":
		r.Operation = protocol.ChatHistory
	case "chat.watermark":
		r.Operation, r.Forward = protocol.ChatChanges, true
	case "chat.changes":
		r.Operation = protocol.ChatChanges
	case "chat.dialogs":
		r.Operation = protocol.ChatDialogs
	case "chat.read":
		r.Operation = protocol.ChatRead
	case "chat.delete":
		r.Operation = protocol.ChatDelete
	case "chat.react":
		r.Operation, r.Text = protocol.ChatReact, c.Text
	case "chat.pin":
		r.Operation = protocol.ChatPin
	case "chat.pins":
		r.Operation = protocol.ChatPins
	case "chat.send":
		r.Operation = protocol.ChatSend
		if len(c.AttachmentIDs) > domain.MaxChatAttachments {
			return nil, errCommand
		}
		for _, value := range c.AttachmentIDs {
			id, err := strconv.ParseInt(value, 10, 64)
			if err != nil || id <= 0 || signed(id) != value {
				return nil, errCommand
			}
			r.AttachmentIDs = append(r.AttachmentIDs, id)
		}
		if err := domain.ValidateChatContent(c.Text, r.AttachmentIDs); err != nil {
			return nil, err
		}
		token, err := hex.DecodeString(c.ClientID)
		if err != nil || len(token) != len(r.ClientID) {
			return nil, errCommand
		}
		copy(r.ClientID[:], token)
		r.Text = c.Text
	}
	page, err := s.Core.ChatRequest(ctx, s.Core.ChatContext()+"|"+signed(view.UserID), r)
	if err != nil {
		return nil, err
	}
	out := &chatPage{ChannelID: c.ChannelID, UserID: c.UserID, Cursor: signed(page.Cursor), HasMore: page.HasMore, Messages: make([]chatMessage, 0, len(page.Messages))}
	for _, m := range page.Messages {
		message := chatMessage{ID: signed(m.ID), SenderID: signed(m.SenderID), SenderName: m.SenderName, Text: m.Text, At: m.SentAtMS, Deleted: m.Deleted, Revision: signed(m.Revision)}
		for _, reaction := range m.Reactions {
			if emoji, ok := domain.ChatReactionEmoji(reaction.Code); ok {
				message.Reactions = append(message.Reactions, reactionView{Emoji: emoji, Count: reaction.Count, Mine: reaction.Mine})
			}
		}
		for _, id := range m.AttachmentIDs {
			message.Attachments = append(message.Attachments, signed(id))
		}
		out.Messages = append(out.Messages, message)
	}
	for _, dialog := range page.Dialogs {
		out.Dialogs = append(out.Dialogs, dialogView{UserID: signed(dialog.UserID), Name: dialog.DisplayName, Unread: dialog.Unread})
	}
	for _, id := range page.Pinned {
		out.PinnedIDs = append(out.PinnedIDs, signed(id))
	}
	return out, nil
}

func commandError(kind string, err error) string {
	var rejection domain.ChatRejection
	var serverError *clientcore.ServerRequestError
	switch {
	case errors.Is(err, errCommand), errors.Is(err, errChatChannel):
		return err.Error()
	case errors.As(err, &rejection):
		return rejection.Error()
	case strings.HasPrefix(kind, "chat.") && errors.As(err, &serverError):
		return serverError.Message
	case errors.Is(err, clientcore.ErrNotConnected):
		return "Связь с Sonoryx ещё не установлена"
	case errors.Is(err, context.DeadlineExceeded):
		return "Sonoryx не ответил вовремя"
	}
	switch kind {
	case "join":
		return "Не удалось войти в канал. Проверьте доступ и свободные места"
	case "mute":
		return "Микрофон недоступен"
	case "deafen":
		return "Не удалось изменить режим прослушивания"
	case "away":
		return "Не удалось изменить статус присутствия"
	case "wake":
		return "Не удалось позвать участника"
	default:
		return "Sonoryx отклонил запрос чата"
	}
}
