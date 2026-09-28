package protocol

import (
	"bytes"
	"testing"

	"uniclog.io/govts/internal/domain"
)

func TestStateEventCodec(t *testing.T) {
	events := []domain.StateEvent{
		{Kind: domain.ParticipantJoined, Revision: 1, Participant: domain.Participant{SessionID: 7, DisplayName: "Алиса"}},
		{Kind: domain.ParticipantLeft, Revision: 2, SessionID: 7},
		{Kind: domain.ParticipantMoved, Revision: 3, SessionID: 7, ChannelID: 2},
		{Kind: domain.ParticipantAudio, Revision: 4, SessionID: 7, Muted: true, Deafened: true},
	}
	for _, event := range events {
		encoded, err := EncodeStateEvent(event)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeStateEvent(encoded)
		if err != nil || got != event {
			t.Fatalf("roundtrip = %+v, %v", got, err)
		}
		for i := 0; i < len(encoded); i++ {
			if _, err := DecodeStateEvent(encoded[:i]); err == nil {
				t.Fatalf("accepted truncated kind %d at %d", event.Kind, i)
			}
		}
		badVersion := bytes.Clone(encoded)
		badVersion[0]++
		badKind := bytes.Clone(encoded)
		badKind[1] = 255
		zeroRevision := bytes.Clone(encoded)
		clear(zeroRevision[2:10])
		zeroID := bytes.Clone(encoded)
		clear(zeroID[10:18])
		for _, invalid := range [][]byte{badVersion, badKind, zeroRevision, zeroID, append(bytes.Clone(encoded), 0), make([]byte, MaxPayloadSize+1)} {
			if _, err := DecodeStateEvent(invalid); err == nil {
				t.Fatalf("accepted malformed event: %x", invalid)
			}
		}
	}
	for _, event := range []domain.StateEvent{
		{Kind: domain.ParticipantJoined, Revision: 1, Participant: domain.Participant{SessionID: 1, DisplayName: "a\x1bb"}},
		{Kind: domain.ParticipantJoined, Revision: 1, Participant: domain.Participant{SessionID: 1, DisplayName: string(bytes.Repeat([]byte{'a'}, 65))}},
		{Kind: domain.ParticipantMoved, Revision: 1, SessionID: 1},
		{Kind: domain.ParticipantLeft, Revision: 1, SessionID: 1, ChannelID: 2},
	} {
		if _, err := EncodeStateEvent(event); err == nil {
			t.Fatalf("accepted invalid domain event %+v", event)
		}
	}
}
