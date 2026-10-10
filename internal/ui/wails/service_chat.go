package wailsui

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

type ChatRequestDTO struct {
	Context   string `json:"context"`
	Operation string `json:"operation"`
	Kind      string `json:"kind"`
	TargetID  string `json:"targetId"`
	Cursor    string `json:"cursor"`
	Forward   bool   `json:"forward"`
	ClientID  string `json:"clientId"`
	Text      string `json:"text"`
}

type ChatMessageDTO struct {
	ID          string `json:"id"`
	SenderID    string `json:"senderId"`
	RecipientID string `json:"recipientId"`
	ChannelID   string `json:"channelId"`
	ClientID    string `json:"clientId"`
	SenderName  string `json:"senderName"`
	Text        string `json:"text"`
	SentAtMS    int64  `json:"sentAtMs"`
}

type ChatDialogDTO struct {
	UserID      string `json:"userId"`
	DisplayName string `json:"displayName"`
	LatestID    string `json:"latestId"`
	ReadID      string `json:"readId"`
	Unread      uint32 `json:"unread"`
}

type ChatPageDTO struct {
	Unread   uint32           `json:"unread"`
	UserID   string           `json:"userId"`
	LatestID string           `json:"latestId"`
	ReadID   string           `json:"readId"`
	Cursor   string           `json:"cursor"`
	HasMore  bool             `json:"hasMore"`
	Messages []ChatMessageDTO `json:"messages"`
	Dialogs  []ChatDialogDTO  `json:"dialogs"`
}

func (s *Service) Chat(request ChatRequestDTO) (ChatPageDTO, error) {
	r := protocol.ChatRequest{Text: request.Text, Forward: request.Forward}
	switch request.Operation {
	case "send":
		r.Operation = protocol.ChatSend
	case "history":
		r.Operation = protocol.ChatHistory
	case "dialogs":
		r.Operation = protocol.ChatDialogs
	case "read":
		r.Operation = protocol.ChatRead
	default:
		return ChatPageDTO{}, errors.New("неизвестная операция чата")
	}
	if r.Operation != protocol.ChatDialogs {
		switch request.Kind {
		case "channel":
			r.Target.Kind = domain.ChatChannel
		case "direct":
			r.Target.Kind = domain.ChatDirect
		default:
			return ChatPageDTO{}, errors.New("некорректный тип чата")
		}
		id, err := strconv.ParseInt(request.TargetID, 10, 64)
		if err != nil || id <= 0 {
			return ChatPageDTO{}, errors.New("некорректный адресат")
		}
		r.Target.ID = id
	}
	if request.Cursor != "" {
		id, err := strconv.ParseInt(request.Cursor, 10, 64)
		if err != nil || id < 0 {
			return ChatPageDTO{}, errors.New("некорректный курсор чата")
		}
		r.Cursor = id
	}
	if request.ClientID != "" {
		b, err := hex.DecodeString(request.ClientID)
		if err != nil || len(b) != 16 {
			return ChatPageDTO{}, errors.New("некорректный идентификатор сообщения")
		}
		copy(r.ClientID[:], b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	p, err := s.client.ChatRequest(ctx, request.Context, r)
	if err != nil {
		return ChatPageDTO{}, err
	}
	dto := ChatPageDTO{UserID: strconv.FormatInt(p.UserID, 10), LatestID: strconv.FormatInt(p.LatestID, 10), ReadID: strconv.FormatInt(p.ReadID, 10), Cursor: strconv.FormatInt(p.Cursor, 10), HasMore: p.HasMore, Messages: make([]ChatMessageDTO, 0, len(p.Messages)), Dialogs: make([]ChatDialogDTO, 0, len(p.Dialogs))}
	dto.Unread = p.Unread
	for _, m := range p.Messages {
		dto.Messages = append(dto.Messages, ChatMessageDTO{ID: strconv.FormatInt(m.ID, 10), SenderID: strconv.FormatInt(m.SenderID, 10), RecipientID: strconv.FormatInt(m.RecipientID, 10), ChannelID: strconv.FormatInt(m.ChannelID, 10), ClientID: hex.EncodeToString(m.ClientID[:]), SenderName: m.SenderName, Text: m.Text, SentAtMS: m.SentAtMS})
	}
	for _, d := range p.Dialogs {
		dto.Dialogs = append(dto.Dialogs, ChatDialogDTO{UserID: strconv.FormatInt(d.UserID, 10), DisplayName: d.DisplayName, LatestID: strconv.FormatInt(d.LatestID, 10), ReadID: strconv.FormatInt(d.ReadID, 10), Unread: d.Unread})
	}
	return dto, nil
}
