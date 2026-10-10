package protocol

import (
	"reflect"
	"strings"
	"testing"
	"uniclog.io/sonoryx/internal/domain"
)

func TestChatRequestRoundTripAndMalformedPayloads(t *testing.T) {
	requests := []ChatRequest{
		{Operation: ChatSend, Target: domain.ChatTarget{Kind: domain.ChatChannel, ID: 1}, ClientID: [16]byte{1}, Text: strings.Repeat("я", 500)},
		{Operation: ChatHistory, Target: domain.ChatTarget{Kind: domain.ChatDirect, ID: 2}, Cursor: 9, Forward: true},
		{Operation: ChatRead, Target: domain.ChatTarget{Kind: domain.ChatChannel, ID: 1}, Cursor: 9},
		{Operation: ChatDialogs, Cursor: 2},
	}
	for _, want := range requests {
		b, err := EncodeChatRequest(want)
		if err != nil || len(b) > MaxPayloadSize {
			t.Fatalf("encode: %v, size %d", err, len(b))
		}
		got, err := DecodeChatRequest(b)
		if err != nil || got != want {
			t.Fatalf("round trip: %+v, %v", got, err)
		}
		for i := range b {
			if _, err := DecodeChatRequest(b[:i]); err == nil {
				t.Fatalf("accepted prefix of length %d", i)
			}
		}
		if _, err := DecodeChatRequest(append(b, 0)); err == nil {
			t.Fatal("accepted trailing byte")
		}
	}
}

func TestChatPageRoundTripAndBudget(t *testing.T) {
	m := domain.ChatMessage{ID: 1, ChannelID: 1, SenderID: 1, ClientID: [16]byte{1}, SentAtMS: 123, SenderName: "alice", Text: strings.Repeat("я", 500)}
	p := domain.ChatPage{UserID: 1, LatestID: 1, Cursor: 1, Messages: []domain.ChatMessage{m}}
	b, err := EncodeChatPage(p)
	if err != nil || len(b) > MaxPayloadSize {
		t.Fatalf("encode: %v, size %d", err, len(b))
	}
	got, err := DecodeChatPage(b)
	if err != nil || !reflect.DeepEqual(got, p) {
		t.Fatalf("round trip: %+v, %v", got, err)
	}
	for i := range b {
		if _, err := DecodeChatPage(b[:i]); err == nil {
			t.Fatalf("accepted prefix %d", i)
		}
	}
	p.Messages = append(p.Messages, m)
	if _, err := EncodeChatPage(p); err == nil {
		t.Fatal("oversize page accepted")
	}
	p.Messages = nil
	p.Dialogs = []domain.ChatDialog{{UserID: 2, DisplayName: "bob", LatestID: 9, ReadID: 8, Unread: 1}}
	b, err = EncodeChatPage(p)
	if err != nil {
		t.Fatal(err)
	}
	got, err = DecodeChatPage(b)
	if err != nil || !reflect.DeepEqual(got, p) {
		t.Fatalf("dialog round trip: %+v, %v", got, err)
	}
}
