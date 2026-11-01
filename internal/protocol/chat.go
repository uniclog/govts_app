package protocol

import (
	"encoding/binary"
	"errors"
	"uniclog.io/sonoryx/internal/domain"
)

const ChatSchemaVersion = 4

type ChatOperation uint8

const (
	ChatSend ChatOperation = iota + 1
	ChatHistory
	ChatDialogs
	ChatRead
	ChatChanges
	ChatDelete
	ChatDeleteFile
	ChatReact
)

type ChatRequest struct {
	AttachmentIDs []int64
	Operation     ChatOperation
	Target        domain.ChatTarget
	Cursor        int64
	Forward       bool
	ClientID      [16]byte
	Text          string
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
	b, err := appendString(b, r.Text, domain.MaxChatTextBytes)
	if err != nil {
		return nil, err
	}
	b = appendAttachmentIDs(b, r.AttachmentIDs)
	if len(b) > MaxPayloadSize {
		return nil, ErrPayloadTooLarge
	}
	return b, nil
}

func DecodeChatRequest(b []byte) (ChatRequest, error) {
	var r ChatRequest
	if len(b) < 39 || len(b) > MaxPayloadSize || b[0] != ChatSchemaVersion || b[3] > 1 {
		return r, errors.New("invalid chat request")
	}
	r.Operation, r.Target.Kind, r.Forward = ChatOperation(b[1]), domain.ChatKind(b[2]), b[3] == 1
	r.Target.ID, r.Cursor = int64(binary.BigEndian.Uint64(b[4:12])), int64(binary.BigEndian.Uint64(b[12:20]))
	copy(r.ClientID[:], b[20:36])
	var err error
	r.Text, b, err = takeString(b[36:], domain.MaxChatTextBytes)
	if err == nil {
		r.AttachmentIDs, b, err = takeAttachmentIDs(b)
	}
	if err != nil || len(b) != 0 {
		return r, errors.New("invalid chat text")
	}
	return r, r.Validate()
}

func (r ChatRequest) Validate() error {
	if r.Operation < ChatSend || r.Operation > ChatReact || r.Cursor < 0 {
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
		return domain.ValidateChatContent(r.Text, r.AttachmentIDs)
	}
	if r.Operation == ChatDelete || r.Operation == ChatDeleteFile {
		if r.Cursor <= 0 || r.Text != "" || r.ClientID != ([16]byte{}) || r.Forward {
			return errors.New("invalid deletion request")
		}
		if r.Operation == ChatDeleteFile {
			if len(r.AttachmentIDs) != 1 || r.AttachmentIDs[0] <= 0 {
				return errors.New("invalid deletion attachment")
			}
		} else if len(r.AttachmentIDs) != 0 {
			return errors.New("unexpected deletion attachments")
		}
		return nil
	}
	if r.Operation == ChatReact {
		// Cursor names the message, Text the emoji, Forward adds or removes it.
		if _, ok := domain.ChatReactionCode(r.Text); !ok || r.Cursor <= 0 || r.ClientID != ([16]byte{}) || len(r.AttachmentIDs) != 0 {
			return errors.New("invalid reaction request")
		}
		return nil
	}
	if r.Operation == ChatChanges && r.Forward && r.Cursor != 0 {
		return errors.New("invalid change watermark request")
	}
	if r.Text != "" || len(r.AttachmentIDs) > 0 || r.ClientID != ([16]byte{}) || (r.Forward && r.Operation != ChatHistory && r.Operation != ChatChanges) {
		return errors.New("unexpected chat data")
	}
	return nil
}

func appendChatMessage(b []byte, m domain.ChatMessage) ([]byte, error) {
	if m.ID <= 0 || m.SenderID <= 0 || m.SentAtMS <= 0 || m.ClientID == ([16]byte{}) || m.ChannelID < 0 || m.RecipientID < 0 || m.RecipientID == m.SenderID || (m.ChannelID == 0) == (m.RecipientID == 0) {
		return nil, errors.New("invalid chat message")
	}
	if m.Revision < 0 || (m.Deleted && (m.Text != "" || len(m.AttachmentIDs) != 0)) {
		return nil, errors.New("invalid deleted message")
	}
	if !m.Deleted {
		if err := domain.ValidateChatContent(m.Text, m.AttachmentIDs); err != nil {
			return nil, err
		}
	}
	for _, id := range []int64{m.ID, m.ChannelID, m.SenderID, m.RecipientID, m.SentAtMS} {
		b = binary.BigEndian.AppendUint64(b, uint64(id))
	}
	b = append(b, m.ClientID[:]...)
	b, err := appendString(b, m.SenderName, domain.MaxParticipantNameBytes)
	if err != nil {
		return nil, err
	}
	flags := uint16(len(m.Text))
	if m.Deleted {
		flags |= 0x8000
	}
	if m.Revision > 0 {
		flags |= 0x4000
	}
	b = binary.BigEndian.AppendUint16(b, flags)
	b = append(b, m.Text...)
	if m.Revision > 0 {
		b = binary.BigEndian.AppendUint64(b, uint64(m.Revision))
	}
	return b, nil
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
		b = appendAttachmentIDs(b, m.AttachmentIDs)
		b, err = appendChatReactions(b, m)
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
		if len(b) < 2 {
			return p, errors.New("short chat text")
		}
		flags := binary.BigEndian.Uint16(b[:2])
		length := int(flags & 0x3fff)
		b = b[2:]
		if length > domain.MaxChatTextBytes || len(b) < length {
			return p, errors.New("invalid chat text")
		}
		m.Text, m.Deleted, b = string(b[:length]), flags&0x8000 != 0, b[length:]
		if flags&0x4000 != 0 {
			if len(b) < 8 {
				return p, errors.New("short chat revision")
			}
			m.Revision = int64(binary.BigEndian.Uint64(b[:8]))
			b = b[8:]
			if m.Revision <= 0 {
				return p, errors.New("invalid chat revision")
			}
		}
		{
			m.AttachmentIDs, b, err = takeAttachmentIDs(b)
			if err != nil {
				return p, err
			}
		}
		m.Reactions, b, err = takeChatReactions(b)
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

func appendAttachmentIDs(b []byte, ids []int64) []byte {
	b = append(b, byte(len(ids)))
	for _, id := range ids {
		b = binary.BigEndian.AppendUint64(b, uint64(id))
	}
	return b
}

// Each reaction is code u8 (0x80 marks the requester's own) ‖ count u16,
// in ascending code order after a count u8.
func appendChatReactions(b []byte, m domain.ChatMessage) ([]byte, error) {
	if len(m.Reactions) > domain.MaxChatReactions || (m.Deleted && len(m.Reactions) != 0) {
		return nil, errors.New("invalid chat reactions")
	}
	b = append(b, byte(len(m.Reactions)))
	var previous uint8
	for _, r := range m.Reactions {
		if _, ok := domain.ChatReactionEmoji(r.Code); !ok || r.Code <= previous || r.Count == 0 {
			return nil, errors.New("invalid chat reaction")
		}
		previous = r.Code
		code := r.Code
		if r.Mine {
			code |= 0x80
		}
		b = append(b, code)
		b = binary.BigEndian.AppendUint16(b, r.Count)
	}
	return b, nil
}

func takeChatReactions(b []byte) ([]domain.ChatReaction, []byte, error) {
	if len(b) < 1 || int(b[0]) > domain.MaxChatReactions || len(b) < 1+int(b[0])*3 {
		return nil, nil, errors.New("invalid chat reactions")
	}
	n := int(b[0])
	b = b[1:]
	if n == 0 {
		return nil, b, nil
	}
	reactions := make([]domain.ChatReaction, n)
	for i := range reactions {
		reactions[i] = domain.ChatReaction{Code: b[0] & 0x7f, Mine: b[0]&0x80 != 0, Count: binary.BigEndian.Uint16(b[1:3])}
		b = b[3:]
	}
	return reactions, b, nil
}

func takeAttachmentIDs(b []byte) ([]int64, []byte, error) {
	if len(b) < 1 || int(b[0]) > domain.MaxChatAttachments || len(b) < 1+int(b[0])*8 {
		return nil, nil, errors.New("invalid attachment IDs")
	}
	n := int(b[0])
	b = b[1:]
	if n == 0 {
		return nil, b, nil
	}
	ids := make([]int64, n)
	for i := range ids {
		ids[i] = int64(binary.BigEndian.Uint64(b[:8]))
		b = b[8:]
	}
	if err := domain.ValidateChatContent("", ids); n > 0 && err != nil {
		return nil, nil, err
	}
	return ids, b, nil
}
