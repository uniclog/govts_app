package domain

import (
	"strings"
	"unicode/utf8"
)

const MaxChatTextBytes = 1000

const MaxChatAttachments = 4

func ValidateChatContent(text string, ids []int64) error {
	if len(ids) > MaxChatAttachments {
		return ChatRejection("слишком много вложений")
	}
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return ChatRejection("некорректные вложения")
		}
		seen[id] = true
	}
	if len(ids) > 0 && strings.TrimSpace(text) == "" && utf8.ValidString(text) && len(text) <= MaxChatTextBytes && !strings.ContainsRune(text, 0) {
		return nil
	}
	return ValidateChatText(text)
}

type ChatKind uint8

const (
	ChatChannel ChatKind = iota + 1
	ChatDirect
)

type ChatTarget struct {
	Kind ChatKind
	ID   int64
}

// ChatRejection is a chat error that is safe to show the user. The server sends
// any other chat error as a generic storage failure and logs the details.
type ChatRejection string

func (e ChatRejection) Error() string { return string(e) }

// ErrChatRecipientUnavailable covers both a missing and a banned recipient, so
// the reply does not tell which accounts exist or are banned.
const ErrChatRecipientUnavailable = ChatRejection("получатель недоступен")

func (t ChatTarget) Validate() error {
	if (t.Kind != ChatChannel && t.Kind != ChatDirect) || t.ID <= 0 {
		return ChatRejection("некорректный адресат сообщения")
	}
	return nil
}

func ValidateChatText(text string) error {
	if !utf8.ValidString(text) || strings.TrimSpace(text) == "" || strings.ContainsRune(text, 0) {
		return ChatRejection("сообщение должно содержать текст UTF-8")
	}
	if len(text) > MaxChatTextBytes {
		return ChatRejection("сообщение превышает 1000 байт UTF-8")
	}
	return nil
}

// chatReactionEmoji is the wire table of ChatReaction.Code, offset by one. Codes
// are stored and sent, so entries may only be appended.
var chatReactionEmoji = [...]string{"👍", "❤️", "😂", "😮", "😢", "🔥", "👏", "🎉"}

const MaxChatReactions = len(chatReactionEmoji)

// ChatReactionCode returns the code of a supported reaction emoji.
func ChatReactionCode(emoji string) (uint8, bool) {
	for i, value := range chatReactionEmoji {
		if value == emoji {
			return uint8(i + 1), true
		}
	}
	return 0, false
}

// ChatReactionEmoji returns the emoji of a reaction code.
func ChatReactionEmoji(code uint8) (string, bool) {
	if code == 0 || int(code) > MaxChatReactions {
		return "", false
	}
	return chatReactionEmoji[code-1], true
}

// ChatReaction is one emoji's total on a message, as seen by the requesting user.
type ChatReaction struct {
	Code  uint8
	Count uint16
	Mine  bool
}

type ChatMessage struct {
	Reactions     []ChatReaction
	Deleted       bool
	Revision      int64
	AttachmentIDs []int64
	ID            int64
	ChannelID     int64
	SenderID      int64
	RecipientID   int64
	ClientID      [16]byte
	SentAtMS      int64
	SenderName    string
	Text          string
}

type ChatDialog struct {
	UserID      int64
	DisplayName string
	LatestID    int64
	ReadID      int64
	Unread      uint32
}

// ChatPage fits one protected control datagram. Cursor is exclusive.
type ChatPage struct {
	UserID   int64
	Unread   uint32
	LatestID int64
	ReadID   int64
	Cursor   int64
	HasMore  bool
	Messages []ChatMessage
	Dialogs  []ChatDialog
}
