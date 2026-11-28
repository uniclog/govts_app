package wailsui

import (
	"strings"
	"testing"

	voiceclient "uniclog.io/sonoryx/internal/clientcore"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

func TestViewDTOExposesLevelsToAdministratorsOnly(t *testing.T) {
	channels := []domain.Channel{{ID: 2, Name: "private", MinJoinLevel: 25, Audio: domain.DefaultAudioProfile()}}
	for _, item := range []struct {
		name         string
		privileges   uint8
		admin, owner bool
	}{
		{"moderator", protocol.PrivilegeKick | protocol.PrivilegeBan | protocol.PrivilegeDrag, false, false},
		{"administrator", protocol.PrivilegeAdmin, true, false},
		{"owner", protocol.PrivilegeOwner, true, true},
	} {
		dto := viewDTO(voiceclient.ClientViewState{JoinLevel: 24, Permissions: item.privileges, Channels: channels}, "")
		if dto.CanAdmin != item.admin || dto.IsOwner != item.owner {
			t.Errorf("%s: canAdmin=%t isOwner=%t", item.name, dto.CanAdmin, dto.IsOwner)
		}
		wantLevel, wantThreshold := uint16(0), uint16(0)
		if item.admin {
			wantLevel, wantThreshold = 24, 25
		}
		if dto.JoinLevel != wantLevel || dto.Channels[0].MinJoinLevel != wantThreshold || dto.Channels[0].CanJoin {
			t.Errorf("%s: joinLevel=%d channel=%+v", item.name, dto.JoinLevel, dto.Channels[0])
		}
	}
}

func TestAdminAccountDTOFlags(t *testing.T) {
	dto := adminAccountDTO(protocol.AdminAccount{
		ID: 7, Fingerprint: [8]byte{0xab, 0xcd}, JoinLevel: 3, Name: "alice", BanReason: "spam",
		Permissions: protocol.PrivilegeBan | protocol.PrivilegeAdmin,
		Flags:       protocol.AdminAccountBanned | protocol.AdminAccountOnline,
	})
	if dto.ID != "7" || dto.Fingerprint != "abcd000000000000" || dto.Kick || !dto.Ban || dto.Drag || !dto.Admin ||
		dto.Owner || !dto.Banned || !dto.Online || dto.BanReason != "spam" || dto.JoinLevel != 3 {
		t.Fatalf("account DTO = %+v", dto)
	}
}

func TestAdminServiceRejectsInvalidInputBeforeSending(t *testing.T) {
	service := &Service{}
	if _, err := service.AdminSetPermission("7", "owner", true); err == nil {
		t.Error("unknown permission accepted")
	}
	if _, err := service.AdminSetPermission("0", "kick", true); err == nil {
		t.Error("zero account accepted")
	}
	if _, err := service.AdminSetJoinLevel("x", 1); err == nil {
		t.Error("malformed account accepted")
	}
	if _, err := service.AdminSetBan("7", true, strings.Repeat("я", protocol.MaxAdminReasonBytes)); err == nil {
		t.Error("ban reason above the byte limit accepted")
	}
	if err := service.AdminSetChannelJoinLevel("", 1); err == nil {
		t.Error("empty channel accepted")
	}
	if _, err := service.AdminAccounts("-1"); err == nil {
		t.Error("negative cursor accepted")
	}
	if _, err := service.AdminAudit("abc"); err == nil {
		t.Error("malformed audit cursor accepted")
	}
	if err := service.AdminDeleteChannel("0"); err == nil {
		t.Error("zero channel deletion accepted")
	}
	if err := service.AdminDeleteAccount("x"); err == nil {
		t.Error("malformed account deletion accepted")
	}
}

func TestAdminChannelRequestConversion(t *testing.T) {
	channel, err := adminChannelRequest(AdminChannelDTO{ParentID: "4", Name: "  room ", Topic: "t", Position: 2, MaxUsers: 9, MinJoinLevel: 3, Bitrate: 64_000, Application: "audio"})
	if err != nil {
		t.Fatal(err)
	}
	want := protocol.AdminChannel{ParentID: 4, Name: "room", Topic: "t", Position: 2, MaxUsers: 9, MinJoinLevel: 3, Bitrate: 64_000, Application: uint8(domain.OpusApplicationAudio)}
	if channel != want {
		t.Fatalf("channel = %+v, want %+v", channel, want)
	}
	if root, err := adminChannelRequest(AdminChannelDTO{Name: "top", Application: "voip"}); err != nil || root.ParentID != 0 || root.Application != uint8(domain.OpusApplicationVoIP) {
		t.Fatalf("root channel = %+v, %v", root, err)
	}
	for name, input := range map[string]AdminChannelDTO{
		"blank name":       {Name: "  ", Application: "voip"},
		"long name":        {Name: strings.Repeat("n", 65), Application: "voip"},
		"long description": {Name: "x", Description: strings.Repeat("d", 513), Application: "voip"},
		"unknown mode":     {Name: "x", Application: "music"},
		"malformed parent": {Name: "x", ParentID: "-1", Application: "voip"},
	} {
		if _, err := adminChannelRequest(input); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	service := &Service{}
	if err := service.AdminUpdateChannel("0", AdminChannelDTO{Name: "x", Application: "voip"}); err == nil {
		t.Error("zero channel ID accepted")
	}
}

func TestServerDTOCarriesDefaultChannel(t *testing.T) {
	dto := viewDTO(voiceclient.ClientViewState{ServerInfo: domain.ServerInfo{Name: "s", DefaultChannelID: 7}}, "")
	if dto.Server.DefaultChannelID != "7" {
		t.Fatalf("server DTO = %+v", dto.Server)
	}
	if empty := viewDTO(voiceclient.ClientViewState{}, ""); empty.Server.DefaultChannelID != "" {
		t.Fatalf("empty server DTO = %+v", empty.Server)
	}
}
