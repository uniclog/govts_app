package protocol

import (
	"bytes"
	"testing"

	"uniclog.io/govts/internal/domain"
)

func TestJoinChannelPayloadRoundTrip(t *testing.T) {
	request, err := EncodeJoinChannelRequest(42)
	if err != nil {
		t.Fatal(err)
	}
	id, err := DecodeJoinChannelRequest(request)
	if err != nil || id != 42 {
		t.Fatalf("request round trip = (%d, %v)", id, err)
	}
	ack, err := EncodeJoinChannelAck(42, 7)
	if err != nil {
		t.Fatal(err)
	}
	id, revision, err := DecodeJoinChannelAck(ack)
	if err != nil || id != 42 || revision != 7 {
		t.Fatalf("ack round trip = (%d, %d, %v)", id, revision, err)
	}
}

func TestJoinChannelDecodersRejectMalformedPayloads(t *testing.T) {
	for _, payload := range [][]byte{nil, make([]byte, JoinChannelRequestSize-1), make([]byte, JoinChannelRequestSize), make([]byte, JoinChannelRequestSize+1)} {
		if _, err := DecodeJoinChannelRequest(payload); err == nil {
			t.Fatalf("accepted malformed join request %x", payload)
		}
	}
	for _, payload := range [][]byte{nil, make([]byte, JoinChannelAckSize-1), make([]byte, JoinChannelAckSize), make([]byte, JoinChannelAckSize+1)} {
		if _, _, err := DecodeJoinChannelAck(payload); err == nil {
			t.Fatalf("accepted malformed join ack %x", payload)
		}
	}
}

func TestSnapshotPayloadRoundTrip(t *testing.T) {
	requests := []SnapshotRequest{{Kind: SnapshotKindMetadata}, {Kind: SnapshotKindChannels, ExpectedRevision: 9, Offset: 3, Limit: 12}, {Kind: SnapshotKindParticipants, ExpectedRevision: 9, Limit: 32}}
	for _, want := range requests {
		payload, err := EncodeSnapshotRequest(want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeSnapshotRequest(payload)
		if err != nil || got != want {
			t.Fatalf("request round trip = (%+v, %v), want %+v", got, err, want)
		}
	}
	channel := domain.Channel{ID: 2, ParentID: 1, Name: "music", Topic: "topic", Description: "description", Position: 4, MaxUsers: 8, Type: domain.ChannelTypePermanent, Audio: domain.DefaultAudioProfile()}
	participant := domain.Participant{SessionID: 11, DisplayName: "alice", ChannelID: 2, Muted: true, Deafened: true}
	responses := []SnapshotResponse{
		{Kind: SnapshotKindMetadata, Status: SnapshotStatusOK, Revision: 9, ServerInfo: domain.ServerInfo{Name: "Server", DefaultChannelID: 2}, ChannelCount: 2, ParticipantCount: 1},
		{Kind: SnapshotKindChannels, Status: SnapshotStatusOK, Revision: 9, Channels: []domain.Channel{channel}},
		{Kind: SnapshotKindParticipants, Status: SnapshotStatusOK, Revision: 9, Participants: []domain.Participant{participant}},
		{Kind: SnapshotKindChannels, Status: SnapshotStatusRevisionChanged, Revision: 10},
	}
	for _, want := range responses {
		payload, err := EncodeSnapshotResponse(want)
		if err != nil {
			t.Fatal(err)
		}
		got, err := DecodeSnapshotResponse(payload)
		if err != nil {
			t.Fatal(err)
		}
		if want.Kind == SnapshotKindMetadata && got.ServerInfo.DefaultChannelID != want.ServerInfo.DefaultChannelID {
			t.Fatalf("default channel ID = %d, want %d", got.ServerInfo.DefaultChannelID, want.ServerInfo.DefaultChannelID)
		}
		encodedAgain, err := EncodeSnapshotResponse(got)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(payload, encodedAgain) {
			t.Fatalf("response changed after round trip: %x != %x", payload, encodedAgain)
		}
	}
}

func TestSnapshotDecodersRejectMalformedPayloads(t *testing.T) {
	for _, payload := range [][]byte{nil, make([]byte, SnapshotRequestSize-1), {255, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}, {SnapshotSchemaVersion, 99, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}} {
		if _, err := DecodeSnapshotRequest(payload); err == nil {
			t.Fatalf("accepted malformed request %x", payload)
		}
	}
	valid, err := EncodeSnapshotResponse(SnapshotResponse{Kind: SnapshotKindMetadata, Status: SnapshotStatusOK, Revision: 1, ServerInfo: domain.ServerInfo{Name: "s"}})
	if err != nil {
		t.Fatal(err)
	}
	malformed := [][]byte{nil, valid[:len(valid)-1], append(append([]byte(nil), valid...), 0)}
	badBool := append([]byte(nil), valid...)
	badBool[15] = 2
	malformed = append(malformed, badBool)
	for _, payload := range malformed {
		if _, err := DecodeSnapshotResponse(payload); err == nil {
			t.Fatalf("accepted malformed response %x", payload)
		}
	}
}

func TestMaximumChannelFitsSnapshotPayload(t *testing.T) {
	channel := domain.Channel{ID: 1, Name: string(bytes.Repeat([]byte{'n'}, domain.MaxChannelNameBytes)), Topic: string(bytes.Repeat([]byte{'t'}, domain.MaxChannelTopicBytes)), Description: string(bytes.Repeat([]byte{'d'}, domain.MaxChannelDescriptionBytes)), Type: domain.ChannelTypePermanent, Audio: domain.DefaultAudioProfile()}
	payload, err := EncodeSnapshotResponse(SnapshotResponse{Kind: SnapshotKindChannels, Status: SnapshotStatusOK, Revision: 1, Channels: []domain.Channel{channel}})
	if err != nil {
		t.Fatal(err)
	}
	if len(payload) > MaxPayloadSize {
		t.Fatalf("maximum channel payload = %d", len(payload))
	}
}

func FuzzDecodeJoinPayloads(f *testing.F) {
	f.Add(make([]byte, 8))
	f.Add(make([]byte, 16))
	f.Fuzz(func(t *testing.T, p []byte) { _, _ = DecodeJoinChannelRequest(p); _, _, _ = DecodeJoinChannelAck(p) })
}
func FuzzDecodeSnapshotRequest(f *testing.F) {
	f.Add(make([]byte, SnapshotRequestSize))
	f.Fuzz(func(t *testing.T, p []byte) { _, _ = DecodeSnapshotRequest(p) })
}
func FuzzDecodeSnapshotResponse(f *testing.F) {
	f.Add(make([]byte, SnapshotResponseHeaderSize))
	f.Fuzz(func(t *testing.T, p []byte) { _, _ = DecodeSnapshotResponse(p) })
}
