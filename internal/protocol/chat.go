package protocol

import (
	"encoding/binary"
	"errors"
	"uniclog.io/sonoryx/internal/domain"
)

const ChatSchemaVersion = 1

type ChatOperation uint8

const (
	ChatSend ChatOperation = iota + 1
	ChatHistory
	ChatDialogs
	ChatRead
)

type ChatRequest struct {
	Operation ChatOperation
	Target    domain.ChatTarget
	Cursor    int64
	Forward   bool
	ClientID  [16]byte
	Text      string
}

func EncodeChatRequest(r ChatRequest) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	b := []byte{ChatSchemaVersion, byte(r.Operation), byte(r.Target.Kind), 0}
	if r.Forward {
		b[3] = 1
	}
	b = binary.BigEndian.AppendUint64(b, uint64(r.Target.ID))
	b = binary.BigEndian.AppendUint64(b, uint64(r.Cursor))
	b = append(b, r.ClientID[:]...)
	return appendString(b, r.Text, domain.MaxChatTextBytes)
}

func DecodeChatRequest(b []byte) (ChatRequest, error) {
	var r ChatRequest
	if len(b) < 38 || len(b) > MaxPayloadSize || b[0] != ChatSchemaVersion || b[3] > 1 {
		return r, errors.New("invalid chat request")
	}
	r.Operation, r.Target.Kind, r.Forward = ChatOperation(b[1]), domain.ChatKind(b[2]), b[3] == 1
	r.Target.ID, r.Cursor = int64(binary.BigEndian.Uint64(b[4:12])), int64(binary.BigEndian.Uint64(b[12:20]))
	copy(r.ClientID[:], b[20:36])
	var err error
	r.Text, b, err = takeString(b[36:], domain.MaxChatTextBytes)
	if err != nil || len(b) != 0 {
		return r, errors.New("invalid chat text")
	}
	return r, r.Validate()
}

func (r ChatRequest) Validate() error {
	if r.Operation < ChatSend || r.Operation > ChatRead || r.Cursor < 0 {
		return errors.New("invalid chat operation")
	}
	if r.Operation != ChatDialogs {
		if err := r.Target.Validate(); err != nil {
			return err
		}
	} else if r.Target != (domain.ChatTarget{}) {
		return errors.New("invalid dialogs target")
	}
	if r.Operation == ChatSend {
		if r.ClientID == ([16]byte{}) || r.Cursor != 0 || r.Forward {
			return errors.New("invalid message identity")
		}
		return domain.ValidateChatText(r.Text)
	}
	if r.Text != "" || r.ClientID != ([16]byte{}) || (r.Forward && r.Operation != ChatHistory) {
		return errors.New("unexpected chat data")
	}
	return nil
}

func appendChatMessage(b []byte, m domain.ChatMessage) ([]byte, error) {
	if m.ID <= 0 || m.SenderID <= 0 || m.SentAtMS <= 0 || m.ClientID == ([16]byte{}) || m.ChannelID < 0 || m.RecipientID < 0 || m.RecipientID == m.SenderID || (m.ChannelID == 0) == (m.RecipientID == 0) {
		return nil, errors.New("invalid chat message")
	}
	if err := domain.ValidateChatText(m.Text); err != nil {
		return nil, err
	}
	for _, id := range []int64{m.ID, m.ChannelID, m.SenderID, m.RecipientID, m.SentAtMS} {
		b = binary.BigEndian.AppendUint64(b, uint64(id))
	}
	b = append(b, m.ClientID[:]...)
	b, err := appendString(b, m.SenderName, domain.MaxParticipantNameBytes)
	if err != nil {
		return nil, err
	}
	return appendString(b, m.Text, domain.MaxChatTextBytes)
}

func EncodeChatPage(p domain.ChatPage) ([]byte, error) {
	if len(p.Messages) > 16 || len(p.Dialogs) > 16 || p.UserID <= 0 || p.LatestID < 0 || p.ReadID < 0 || p.Cursor < 0 {
		return nil, errors.New("invalid chat page")
	}
	b := []byte{ChatSchemaVersion, 0, byte(len(p.Messages)), byte(len(p.Dialogs))}
	if p.HasMore {
		b[1] = 1
	}
	for _, id := range []int64{p.UserID, p.LatestID, p.ReadID, p.Cursor} {
		b = binary.BigEndian.AppendUint64(b, uint64(id))
	}
	b = binary.BigEndian.AppendUint32(b, p.Unread)
	var err error
	for _, m := range p.Messages {
		b, err = appendChatMessage(b, m)
		if err != nil {
			return nil, err
		}
	}
	for _, d := range p.Dialogs {
		if d.UserID <= 0 || d.LatestID < 0 || d.ReadID < 0 {
			return nil, errors.New("invalid chat dialog")
		}
		for _, id := range []int64{d.UserID, d.LatestID, d.ReadID} {
			b = binary.BigEndian.AppendUint64(b, uint64(id))
		}
		b = binary.BigEndian.AppendUint32(b, d.Unread)
		b, err = appendString(b, d.DisplayName, domain.MaxParticipantNameBytes)
		if err != nil {
			return nil, err
		}
	}
	if len(b) > MaxPayloadSize {
		return nil, ErrPayloadTooLarge
	}
	return b, nil
}

func DecodeChatPage(b []byte) (domain.ChatPage, error) {
	var p domain.ChatPage
	if len(b) < 40 || len(b) > MaxPayloadSize || b[0] != ChatSchemaVersion || b[1] > 1 || b[2] > 16 || b[3] > 16 {
		return p, errors.New("invalid chat page")
	}
	nm, nd := int(b[2]), int(b[3])
	p.HasMore = b[1] == 1
	p.UserID, p.LatestID, p.ReadID, p.Cursor = int64(binary.BigEndian.Uint64(b[4:12])), int64(binary.BigEndian.Uint64(b[12:20])), int64(binary.BigEndian.Uint64(b[20:28])), int64(binary.BigEndian.Uint64(b[28:36]))
	p.Unread = binary.BigEndian.Uint32(b[36:40])
	b = b[40:]
	for i := 0; i < nm; i++ {
		if len(b) < 56 {
			return p, errors.New("short chat message")
		}
		m := domain.ChatMessage{ID: int64(binary.BigEndian.Uint64(b[:8])), ChannelID: int64(binary.BigEndian.Uint64(b[8:16])), SenderID: int64(binary.BigEndian.Uint64(b[16:24])), RecipientID: int64(binary.BigEndian.Uint64(b[24:32])), SentAtMS: int64(binary.BigEndian.Uint64(b[32:40]))}
		copy(m.ClientID[:], b[40:56])
		b = b[56:]
		var err error
		m.SenderName, b, err = takeString(b, domain.MaxParticipantNameBytes)
		if err != nil {
			return p, err
		}
		m.Text, b, err = takeString(b, domain.MaxChatTextBytes)
		if err != nil {
			return p, err
		}
		p.Messages = append(p.Messages, m)
	}
	for i := 0; i < nd; i++ {
		if len(b) < 28 {
			return p, errors.New("short chat dialog")
		}
		d := domain.ChatDialog{UserID: int64(binary.BigEndian.Uint64(b[:8])), LatestID: int64(binary.BigEndian.Uint64(b[8:16])), ReadID: int64(binary.BigEndian.Uint64(b[16:24])), Unread: binary.BigEndian.Uint32(b[24:28])}
		var err error
		d.DisplayName, b, err = takeString(b[28:], domain.MaxParticipantNameBytes)
		if err != nil {
			return p, err
		}
		p.Dialogs = append(p.Dialogs, d)
	}
	if len(b) != 0 {
		return p, errors.New("trailing chat data")
	}
	// Reuse encoder validation on every received field.
	_, err := EncodeChatPage(p)
	return p, err
}
