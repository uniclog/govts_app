package wailsui

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"

	voiceclient "uniclog.io/govts/internal/client"
	"uniclog.io/govts/internal/domain"
)

func TestViewDTOKeepsUint64IdentifiersExact(t *testing.T) {
	view := voiceclient.ClientViewState{
		ConnectionStatus: voiceclient.ConnectionConnected,
		Revision:         domain.StateRevision(math.MaxUint64),
		SessionID:        math.MaxUint64,
		ChannelID:        domain.ChannelID(uint64(1) << 53),
		Channels: []domain.Channel{{
			ID:       domain.ChannelID(math.MaxUint64),
			ParentID: domain.ChannelID(uint64(1) << 53),
			Audio:    domain.DefaultAudioProfile(),
		}},
		Participants: []domain.Participant{{
			SessionID:   math.MaxUint64,
			DisplayName: "alice",
			ChannelID:   domain.ChannelID(math.MaxUint64),
		}},
		Speaking: map[uint64]bool{math.MaxUint64: true},
	}

	dto := viewDTO(view, "")
	if dto.Revision != "18446744073709551615" || dto.SessionID != "18446744073709551615" {
		t.Fatalf("top-level identifiers lost precision: %#v", dto)
	}
	if dto.ChannelID != "9007199254740992" || dto.Channels[0].ID != "18446744073709551615" {
		t.Fatalf("channel identifiers lost precision: %#v", dto.Channels[0])
	}
	view.Muted = true
	view.Deafened = true
	view.CaptureAvailable = false
	dto = viewDTO(view, "")
	if !dto.Participants[0].Speaking || !dto.Participants[0].Local || !dto.Participants[0].Muted || !dto.Participants[0].Deafened || dto.Audio.CaptureAvailable {
		t.Fatalf("participant flags = %#v, audio = %#v", dto.Participants[0], dto.Audio)
	}
}

func TestViewDTOUsesNonNilArrays(t *testing.T) {
	dto := viewDTO(voiceclient.ClientViewState{}, "")
	if dto.Channels == nil || dto.Participants == nil {
		t.Fatalf("nil arrays in DTO: %#v", dto)
	}
}

func TestViewDTOExposesOnlyChannelAccessNotLevels(t *testing.T) {
	view := voiceclient.ClientViewState{JoinLevel: 24, Channels: []domain.Channel{
		{ID: 1, Name: "default", Audio: domain.DefaultAudioProfile()},
		{ID: 2, Name: "private", MinJoinLevel: 25, Audio: domain.DefaultAudioProfile()},
	}}
	dto := viewDTO(view, "")
	if !dto.Channels[0].CanJoin || dto.Channels[1].CanJoin {
		t.Fatalf("channel access flags = %+v", dto.Channels)
	}
	encoded, err := json.Marshal(dto)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("joinLevel")) || bytes.Contains(encoded, []byte("minJoinLevel")) {
		t.Fatalf("UI response leaked numeric levels: %s", encoded)
	}
}

func TestParseUint64RejectsMalformedAndZeroIdentifiers(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "1.5", "18446744073709551616"} {
		if _, err := parseUint64(value, "channelId", false); err == nil {
			t.Fatalf("accepted channel ID %q", value)
		}
	}
	if got, err := parseUint64("18446744073709551615", "channelId", false); err != nil || got != math.MaxUint64 {
		t.Fatalf("max uint64 = %d, %v", got, err)
	}
}
