package wailsui

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"uniclog.io/sonoryx/internal/auth"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

type AdminAccountDTO struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Fingerprint string `json:"fingerprint"`
	JoinLevel   uint16 `json:"joinLevel"`
	Kick        bool   `json:"kick"`
	Ban         bool   `json:"ban"`
	Drag        bool   `json:"drag"`
	Admin       bool   `json:"admin"`
	Owner       bool   `json:"owner"`
	Banned      bool   `json:"banned"`
	Online      bool   `json:"online"`
	BanReason   string `json:"banReason"`
}

type AdminAccountsPageDTO struct {
	Accounts []AdminAccountDTO `json:"accounts"`
	Next     string            `json:"next"`
}

type AdminAuditEntryDTO struct {
	ID           string `json:"id"`
	HappenedAtMS int64  `json:"happenedAtMs"`
	Actor        string `json:"actor"`
	Action       string `json:"action"`
	TargetUserID string `json:"targetUserId"`
	Detail       string `json:"detail"`
}

type AdminAuditPageDTO struct {
	Entries []AdminAuditEntryDTO `json:"entries"`
	Next    string               `json:"next"`
}

func adminAccountDTO(a protocol.AdminAccount) AdminAccountDTO {
	return AdminAccountDTO{
		ID:          formatUint64(a.ID),
		Name:        a.Name,
		Fingerprint: hex.EncodeToString(a.Fingerprint[:]),
		JoinLevel:   a.JoinLevel,
		Kick:        a.Permissions&protocol.PrivilegeKick != 0,
		Ban:         a.Permissions&protocol.PrivilegeBan != 0,
		Drag:        a.Permissions&protocol.PrivilegeDrag != 0,
		Admin:       a.Permissions&protocol.PrivilegeAdmin != 0,
		Owner:       a.Flags&protocol.AdminAccountOwner != 0,
		Banned:      a.Flags&protocol.AdminAccountBanned != 0,
		Online:      a.Flags&protocol.AdminAccountOnline != 0,
		BanReason:   a.BanReason,
	}
}

func (s *Service) administer(request protocol.AdminRequest) (protocol.AdminResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.Administer(ctx, request)
}

func (s *Service) administerAccount(request protocol.AdminRequest) (AdminAccountDTO, error) {
	response, err := s.administer(request)
	if err != nil {
		return AdminAccountDTO{}, err
	}
	if len(response.Accounts) != 1 {
		return AdminAccountDTO{}, errors.New("некорректный ответ сервера")
	}
	return adminAccountDTO(response.Accounts[0]), nil
}

// AdminAccounts returns the page of accounts after the account ID in cursor.
func (s *Service) AdminAccounts(cursor string) (AdminAccountsPageDTO, error) {
	after, err := parseUint64(cursor, "cursor", true)
	if err != nil {
		return AdminAccountsPageDTO{}, err
	}
	response, err := s.administer(protocol.AdminRequest{Operation: protocol.AdminAccounts, Cursor: after})
	if err != nil {
		return AdminAccountsPageDTO{}, err
	}
	page := AdminAccountsPageDTO{Accounts: make([]AdminAccountDTO, 0, len(response.Accounts))}
	for _, account := range response.Accounts {
		page.Accounts = append(page.Accounts, adminAccountDTO(account))
	}
	if response.Next != 0 {
		page.Next = formatUint64(response.Next)
	}
	return page, nil
}

// AdminSetPermission grants or revokes one of "kick", "ban", "drag", "admin".
func (s *Service) AdminSetPermission(accountID, permission string, enabled bool) (AdminAccountDTO, error) {
	id, err := parseUint64(accountID, "accountId", false)
	if err != nil {
		return AdminAccountDTO{}, err
	}
	var bit uint8
	switch permission {
	case "kick":
		bit = protocol.PrivilegeKick
	case "ban":
		bit = protocol.PrivilegeBan
	case "drag":
		bit = protocol.PrivilegeDrag
	case "admin":
		bit = protocol.PrivilegeAdmin
	default:
		return AdminAccountDTO{}, errors.New("неизвестное право")
	}
	return s.administerAccount(protocol.AdminRequest{Operation: protocol.AdminSetPermission, TargetID: id, Permission: bit, Enabled: enabled})
}

func (s *Service) AdminSetJoinLevel(accountID string, level uint16) (AdminAccountDTO, error) {
	id, err := parseUint64(accountID, "accountId", false)
	if err != nil {
		return AdminAccountDTO{}, err
	}
	return s.administerAccount(protocol.AdminRequest{Operation: protocol.AdminSetJoinLevel, TargetID: id, Level: level})
}

func (s *Service) AdminSetBan(accountID string, banned bool, reason string) (AdminAccountDTO, error) {
	id, err := parseUint64(accountID, "accountId", false)
	if err != nil {
		return AdminAccountDTO{}, err
	}
	if !banned {
		reason = ""
	}
	if len(reason) > protocol.MaxAdminReasonBytes {
		return AdminAccountDTO{}, errors.New("причина бана слишком длинная")
	}
	return s.administerAccount(protocol.AdminRequest{Operation: protocol.AdminSetBan, TargetID: id, Enabled: banned, Reason: reason})
}

func (s *Service) AdminSetChannelJoinLevel(channelID string, level uint16) error {
	id, err := parseUint64(channelID, "channelId", false)
	if err != nil {
		return err
	}
	_, err = s.administer(protocol.AdminRequest{Operation: protocol.AdminSetChannelJoinLevel, TargetID: id, Level: level})
	return err
}

// AdminAudit returns the page of audit entries older than the entry ID in
// cursor; an empty cursor starts from the newest entry.
func (s *Service) AdminAudit(cursor string) (AdminAuditPageDTO, error) {
	before, err := parseUint64(cursor, "cursor", true)
	if err != nil {
		return AdminAuditPageDTO{}, err
	}
	response, err := s.administer(protocol.AdminRequest{Operation: protocol.AdminAudit, Cursor: before})
	if err != nil {
		return AdminAuditPageDTO{}, err
	}
	page := AdminAuditPageDTO{Entries: make([]AdminAuditEntryDTO, 0, len(response.Audit))}
	for _, entry := range response.Audit {
		target := ""
		if entry.TargetUserID != 0 {
			target = formatUint64(entry.TargetUserID)
		}
		page.Entries = append(page.Entries, AdminAuditEntryDTO{ID: formatUint64(entry.ID), HappenedAtMS: entry.HappenedAtMS, Actor: entry.Actor, Action: entry.Action, TargetUserID: target, Detail: entry.Detail})
	}
	if response.Next != 0 {
		page.Next = formatUint64(response.Next)
	}
	return page, nil
}

// AdminChannelDTO holds the editable settings of a channel. Application is
// "voip" or "audio"; Bitrate is in bit/s.
type AdminChannelDTO struct {
	ParentID     string `json:"parentId"`
	Name         string `json:"name"`
	Topic        string `json:"topic"`
	Description  string `json:"description"`
	Position     uint32 `json:"position"`
	MaxUsers     uint32 `json:"maxUsers"`
	MinJoinLevel uint16 `json:"minJoinLevel"`
	Bitrate      uint32 `json:"bitrate"`
	Application  string `json:"application"`
}

func adminChannelRequest(input AdminChannelDTO) (protocol.AdminChannel, error) {
	parent, err := parseUint64(input.ParentID, "parentId", true)
	if err != nil {
		return protocol.AdminChannel{}, err
	}
	channel := protocol.AdminChannel{
		ParentID: parent, Position: input.Position, MaxUsers: input.MaxUsers, MinJoinLevel: input.MinJoinLevel,
		Bitrate: input.Bitrate, Name: strings.TrimSpace(input.Name), Topic: input.Topic, Description: input.Description,
	}
	switch input.Application {
	case "voip":
		channel.Application = uint8(domain.OpusApplicationVoIP)
	case "audio":
		channel.Application = uint8(domain.OpusApplicationAudio)
	default:
		return protocol.AdminChannel{}, errors.New("неизвестный режим Opus")
	}
	if channel.Name == "" || len(channel.Name) > domain.MaxChannelNameBytes || len(channel.Topic) > domain.MaxChannelTopicBytes || len(channel.Description) > domain.MaxChannelDescriptionBytes {
		return protocol.AdminChannel{}, errors.New("некорректное название, тема или описание канала")
	}
	return channel, nil
}

// AdminCreateChannel creates a channel and returns its ID.
func (s *Service) AdminCreateChannel(input AdminChannelDTO) (string, error) {
	channel, err := adminChannelRequest(input)
	if err != nil {
		return "", err
	}
	response, err := s.administer(protocol.AdminRequest{Operation: protocol.AdminCreateChannel, Channel: channel})
	if err != nil {
		return "", err
	}
	return formatUint64(response.Next), nil
}

// AdminUpdateChannel replaces the settings of a channel, including its parent
// and position.
func (s *Service) AdminUpdateChannel(channelID string, input AdminChannelDTO) error {
	id, err := parseUint64(channelID, "channelId", false)
	if err != nil {
		return err
	}
	channel, err := adminChannelRequest(input)
	if err != nil {
		return err
	}
	_, err = s.administer(protocol.AdminRequest{Operation: protocol.AdminUpdateChannel, TargetID: id, Channel: channel})
	return err
}

// AdminDeleteAccount forgets an account and disconnects it; its key
// registers a new account on the next connection.
func (s *Service) AdminDeleteAccount(accountID string) error {
	id, err := parseUint64(accountID, "accountId", false)
	if err != nil {
		return err
	}
	_, err = s.administer(protocol.AdminRequest{Operation: protocol.AdminDeleteAccount, TargetID: id})
	return err
}

// AdminDeleteChannel removes a channel without subchannels; the server moves
// its members to the default channel.
func (s *Service) AdminDeleteChannel(channelID string) error {
	id, err := parseUint64(channelID, "channelId", false)
	if err != nil {
		return err
	}
	_, err = s.administer(protocol.AdminRequest{Operation: protocol.AdminDeleteChannel, TargetID: id})
	return err
}

// AdminServerPassword returns only whether admission requires a password.
func (s *Service) AdminServerPassword() (bool, error) {
	response, err := s.administer(protocol.AdminRequest{Operation: protocol.AdminServerPassword})
	return response.Next == 1, err
}

// AdminSetServerPassword changes or clears the owner-only admission setting.
func (s *Service) AdminSetServerPassword(password string) error {
	if err := auth.ValidatePassword(password); err != nil {
		return err
	}
	_, err := s.administer(protocol.AdminRequest{Operation: protocol.AdminSetServerPassword, Password: password})
	return err
}
