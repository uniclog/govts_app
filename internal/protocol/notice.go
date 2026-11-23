package protocol

import (
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf8"

	"uniclog.io/sonoryx/internal/domain"
)

// Notice preferences, the one-byte payload of PacketNoticePrefs and its ack.
const (
	// NoticeAnnounce asks the server to tell the others that this session has
	// arrived. It acts once per session; a later request without it does not
	// take the arrival back. An invisible client never sets it.
	NoticeAnnounce byte = 1 << 0
	// NoticeSubscribed asks for message notices from every channel the account
	// may join, not only from the channel the session is in.
	NoticeSubscribed byte = 1 << 1
	noticePrefsMask       = NoticeAnnounce | NoticeSubscribed
)

// Notice kinds.
const (
	NoticeArrival byte = 1
	NoticeMessage byte = 2
)

const noticeVersion = 1

// MaxNoticeTextBytes bounds the message excerpt of a notice. A popup shows a
// single line, so a longer text would only cost bandwidth.
const MaxNoticeTextBytes = 240

// Notice is a server hint for a desktop notification. It is never stored and
// is not part of the replicated state: a lost notice is simply not shown.
type Notice struct {
	Kind byte
	// UserID and Name are the arriving user or the message sender.
	UserID int64
	Name   string
	// SessionID identifies an arrival: the same arrival reaching a client
	// twice has the same session; a new connection has a new one.
	SessionID uint64
	// Message notices only. Target is the conversation as the recipient sees
	// it: the channel, or the direct dialog with the sender.
	Target      domain.ChatTarget
	ChannelName string
	MessageID   int64
	Attachments bool
	Text        string
}

func EncodeNoticePrefs(flags byte) []byte { return []byte{flags & noticePrefsMask} }

func DecodeNoticePrefs(payload []byte) (byte, error) {
	if len(payload) != 1 || payload[0]&^noticePrefsMask != 0 {
		return 0, errors.New("invalid notice preferences")
	}
	return payload[0], nil
}

// NoticeExcerpt shortens text to the notice limit on a UTF-8 boundary and
// folds line breaks, so the popup line ends with an ellipsis, not a break.
func NoticeExcerpt(text string) (string, bool) {
	text = strings.Join(strings.Fields(text), " ")
	if len(text) <= MaxNoticeTextBytes {
		return text, false
	}
	text = text[:MaxNoticeTextBytes]
	for !utf8.ValidString(text) {
		text = text[:len(text)-1]
	}
	return text, true
}

// EncodeNotice writes version ‖ kind ‖ user u64 ‖ name. An arrival adds
// session u64; a message notice adds target kind u8 ‖ target id u64 ‖
// message id u64 ‖ flags u8 ‖ channel name ‖ text, where strings are
// length-prefixed (u8, and u16 for the text).
func EncodeNotice(n Notice) ([]byte, error) {
	if err := validateNotice(n); err != nil {
		return nil, err
	}
	b := []byte{noticeVersion, n.Kind}
	b = binary.BigEndian.AppendUint64(b, uint64(n.UserID))
	b = append(b, byte(len(n.Name)))
	b = append(b, n.Name...)
	if n.Kind == NoticeArrival {
		return binary.BigEndian.AppendUint64(b, n.SessionID), nil
	}
	b = append(b, byte(n.Target.Kind))
	b = binary.BigEndian.AppendUint64(b, uint64(n.Target.ID))
	b = binary.BigEndian.AppendUint64(b, uint64(n.MessageID))
	var flags byte
	if n.Attachments {
		flags |= 1
	}
	b = append(b, flags, byte(len(n.ChannelName)))
	b = append(b, n.ChannelName...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(n.Text)))
	return append(b, n.Text...), nil
}

func DecodeNotice(b []byte) (Notice, error) {
	var n Notice
	if len(b) < 11 || b[0] != noticeVersion {
		return n, errors.New("invalid notice header")
	}
	n.Kind = b[1]
	n.UserID = int64(binary.BigEndian.Uint64(b[2:10]))
	name, rest, ok := takeShortString(b[10:])
	if !ok {
		return n, errors.New("invalid notice name")
	}
	n.Name = name
	switch n.Kind {
	case NoticeArrival:
		// The session id was added later; an arrival without it has id 0.
		if len(rest) != 0 && len(rest) != 8 {
			return n, errors.New("invalid arrival notice")
		}
		if len(rest) == 8 {
			n.SessionID = binary.BigEndian.Uint64(rest)
		}
		rest = nil
	case NoticeMessage:
		if len(rest) < 18 {
			return n, errors.New("invalid message notice")
		}
		n.Target = domain.ChatTarget{Kind: domain.ChatKind(rest[0]), ID: int64(binary.BigEndian.Uint64(rest[1:9]))}
		n.MessageID = int64(binary.BigEndian.Uint64(rest[9:17]))
		if rest[17]&^1 != 0 {
			return n, errors.New("invalid message notice flags")
		}
		n.Attachments = rest[17]&1 != 0
		n.ChannelName, rest, ok = takeShortString(rest[18:])
		if !ok || len(rest) < 2 {
			return n, errors.New("invalid message notice channel")
		}
		size := int(binary.BigEndian.Uint16(rest[:2]))
		if len(rest) != 2+size {
			return n, errors.New("invalid message notice text")
		}
		n.Text = string(rest[2:])
		rest = nil
	default:
		return n, errors.New("unknown notice kind")
	}
	if len(rest) != 0 {
		return n, errors.New("trailing notice data")
	}
	return n, validateNotice(n)
}

func takeShortString(b []byte) (string, []byte, bool) {
	if len(b) < 1 || len(b) < 1+int(b[0]) {
		return "", nil, false
	}
	return string(b[1 : 1+int(b[0])]), b[1+int(b[0]):], true
}

func validateNotice(n Notice) error {
	if n.UserID <= 0 || n.Name == "" || len(n.Name) > domain.MaxParticipantNameBytes || !utf8.ValidString(n.Name) {
		return errors.New("invalid notice sender")
	}
	switch n.Kind {
	case NoticeArrival:
		if n.Target != (domain.ChatTarget{}) || n.ChannelName != "" || n.MessageID != 0 || n.Attachments || n.Text != "" {
			return errors.New("invalid arrival notice")
		}
	case NoticeMessage:
		if err := n.Target.Validate(); err != nil || n.MessageID <= 0 || n.SessionID != 0 {
			return errors.New("invalid message notice target")
		}
		if (n.Target.Kind == domain.ChatChannel) == (n.ChannelName == "") || len(n.ChannelName) > domain.MaxChannelNameBytes || !utf8.ValidString(n.ChannelName) {
			return errors.New("invalid message notice channel")
		}
		if len(n.Text) > MaxNoticeTextBytes || !utf8.ValidString(n.Text) || (n.Text == "" && !n.Attachments) {
			return errors.New("invalid message notice text")
		}
	default:
		return errors.New("unknown notice kind")
	}
	return nil
}
