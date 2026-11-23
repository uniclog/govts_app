package protocol

import (
	"encoding/binary"
	"strings"
	"testing"
	"unicode/utf8"

	"uniclog.io/sonoryx/internal/domain"
)

func TestNoticePrefsAcceptOnlyKnownFlags(t *testing.T) {
	for _, flags := range []byte{0, NoticeAnnounce, NoticeSubscribed, NoticeAnnounce | NoticeSubscribed} {
		got, err := DecodeNoticePrefs(EncodeNoticePrefs(flags))
		if err != nil || got != flags {
			t.Fatalf("prefs %08b: got %08b, %v", flags, got, err)
		}
	}
	if got := EncodeNoticePrefs(0xff); len(got) != 1 || got[0] != NoticeAnnounce|NoticeSubscribed {
		t.Fatalf("EncodeNoticePrefs(0xff) = %v, want only known bits", got)
	}
	for _, payload := range [][]byte{nil, {}, {4}, {0x80}, {1, 0}} {
		if _, err := DecodeNoticePrefs(payload); err == nil {
			t.Fatalf("DecodeNoticePrefs(%v) accepted", payload)
		}
	}
}

func TestArrivalNoticeRoundTrip(t *testing.T) {
	notice := Notice{Kind: NoticeArrival, UserID: 42, Name: "Лиза", SessionID: 0x0102030405060708}
	payload, err := EncodeNotice(notice)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeNotice(payload)
	if err != nil {
		t.Fatal(err)
	}
	if got != notice {
		t.Fatalf("DecodeNotice() = %+v, want %+v", got, notice)
	}
}

// A server built before the session id was added sends the arrival without
// it; the client still shows it, with session id 0.
func TestArrivalNoticeWithoutSessionIsAccepted(t *testing.T) {
	payload, err := EncodeNotice(Notice{Kind: NoticeArrival, UserID: 7, Name: "bob", SessionID: 9})
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeNotice(payload[:len(payload)-8])
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != 0 || got.UserID != 7 || got.Name != "bob" {
		t.Fatalf("legacy arrival = %+v", got)
	}
	if _, err := DecodeNotice(payload[:len(payload)-3]); err == nil {
		t.Fatal("arrival with a partial session id accepted")
	}
}

func TestMessageNoticeRoundTrip(t *testing.T) {
	for _, notice := range []Notice{
		{Kind: NoticeMessage, UserID: 3, Name: "alice", Target: domain.ChatTarget{Kind: domain.ChatChannel, ID: 5}, ChannelName: "general", MessageID: 99, Text: "привет"},
		{Kind: NoticeMessage, UserID: 3, Name: "alice", Target: domain.ChatTarget{Kind: domain.ChatDirect, ID: 3}, MessageID: 100, Attachments: true},
		{Kind: NoticeMessage, UserID: 3, Name: "alice", Target: domain.ChatTarget{Kind: domain.ChatDirect, ID: 3}, MessageID: 101, Text: "file", Attachments: true},
	} {
		payload, err := EncodeNotice(notice)
		if err != nil {
			t.Fatalf("EncodeNotice(%+v): %v", notice, err)
		}
		got, err := DecodeNotice(payload)
		if err != nil {
			t.Fatalf("DecodeNotice(%+v): %v", notice, err)
		}
		if got != notice {
			t.Fatalf("DecodeNotice() = %+v, want %+v", got, notice)
		}
		if _, err := DecodeNotice(append(payload, 0)); err == nil {
			t.Fatalf("trailing byte accepted for %+v", notice)
		}
	}
}

func TestNoticeValidation(t *testing.T) {
	channel := domain.ChatTarget{Kind: domain.ChatChannel, ID: 1}
	direct := domain.ChatTarget{Kind: domain.ChatDirect, ID: 1}
	for name, notice := range map[string]Notice{
		"unknown kind":          {Kind: 9, UserID: 1, Name: "a"},
		"no user":               {Kind: NoticeArrival, Name: "a", SessionID: 1},
		"no name":               {Kind: NoticeArrival, UserID: 1, SessionID: 1},
		"long name":             {Kind: NoticeArrival, UserID: 1, Name: strings.Repeat("a", domain.MaxParticipantNameBytes+1), SessionID: 1},
		"arrival with text":     {Kind: NoticeArrival, UserID: 1, Name: "a", SessionID: 1, Text: "x"},
		"channel without name":  {Kind: NoticeMessage, UserID: 1, Name: "a", Target: channel, MessageID: 1, Text: "x"},
		"direct with channel":   {Kind: NoticeMessage, UserID: 1, Name: "a", Target: direct, ChannelName: "c", MessageID: 1, Text: "x"},
		"no message id":         {Kind: NoticeMessage, UserID: 1, Name: "a", Target: direct, Text: "x"},
		"message with session":  {Kind: NoticeMessage, UserID: 1, Name: "a", Target: direct, MessageID: 1, SessionID: 1, Text: "x"},
		"empty text no files":   {Kind: NoticeMessage, UserID: 1, Name: "a", Target: direct, MessageID: 1},
		"text over the limit":   {Kind: NoticeMessage, UserID: 1, Name: "a", Target: direct, MessageID: 1, Text: strings.Repeat("x", MaxNoticeTextBytes+1)},
		"invalid target":        {Kind: NoticeMessage, UserID: 1, Name: "a", Target: domain.ChatTarget{Kind: 3, ID: 1}, MessageID: 1, Text: "x"},
		"invalid UTF-8 in text": {Kind: NoticeMessage, UserID: 1, Name: "a", Target: direct, MessageID: 1, Text: "\xff"},
	} {
		if _, err := EncodeNotice(notice); err == nil {
			t.Errorf("%s: EncodeNotice accepted %+v", name, notice)
		}
	}
}

func TestDecodeNoticeRejectsMalformedPayloads(t *testing.T) {
	valid, err := EncodeNotice(Notice{Kind: NoticeMessage, UserID: 1, Name: "a", Target: domain.ChatTarget{Kind: domain.ChatChannel, ID: 2}, ChannelName: "c", MessageID: 3, Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	wrongVersion := append([]byte(nil), valid...)
	wrongVersion[0] = 2
	longText := append([]byte(nil), valid...)
	binary.BigEndian.PutUint16(longText[len(longText)-4:], 3)
	badFlags := append([]byte(nil), valid...)
	badFlags[1+1+8+1+1+1+8+8] = 2
	for name, payload := range map[string][]byte{
		"empty":         nil,
		"short":         valid[:10],
		"version":       wrongVersion,
		"truncated":     valid[:len(valid)-1],
		"text length":   longText,
		"unknown flags": badFlags,
	} {
		if _, err := DecodeNotice(payload); err == nil {
			t.Errorf("%s: DecodeNotice accepted %v", name, payload)
		}
	}
}

func TestNoticeExcerpt(t *testing.T) {
	text, cut := NoticeExcerpt("  строка\nвторая\t  третья  ")
	if text != "строка вторая третья" || cut {
		t.Fatalf("NoticeExcerpt folded = %q, %t", text, cut)
	}
	// Two-byte runes: the limit falls inside one of them.
	long := strings.Repeat("я", MaxNoticeTextBytes)
	text, cut = NoticeExcerpt("x" + long)
	if !cut || len(text) > MaxNoticeTextBytes || !utf8.ValidString(text) || len(text) != MaxNoticeTextBytes-1 {
		t.Fatalf("NoticeExcerpt cut to %d bytes, valid=%t, cut=%t", len(text), utf8.ValidString(text), cut)
	}
}
