package wailsui

import (
	"strconv"
	"time"

	"uniclog.io/sonoryx/internal/audio"
	voiceclient "uniclog.io/sonoryx/internal/client"
	"uniclog.io/sonoryx/internal/clientapp"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

type ServerDTO struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	// DefaultChannelID is the channel new sessions join; it stays unrestricted.
	DefaultChannelID string `json:"defaultChannelId,omitempty"`
}

type AudioProfileDTO struct {
	Codec           string `json:"codec"`
	SampleRate      uint32 `json:"sampleRate"`
	Channels        uint8  `json:"channels"`
	FrameDurationMS uint16 `json:"frameDurationMs"`
	Bitrate         uint32 `json:"bitrate"`
	Application     string `json:"application"`
}

type ChannelDTO struct {
	ID           string          `json:"id"`
	ParentID     string          `json:"parentId"`
	Name         string          `json:"name"`
	Topic        string          `json:"topic"`
	Description  string          `json:"description"`
	Position     uint32          `json:"position"`
	MaxUsers     uint32          `json:"maxUsers"`
	MinJoinLevel uint16          `json:"minJoinLevel,omitempty"` // administrators only
	CanJoin      bool            `json:"canJoin"`
	Audio        AudioProfileDTO `json:"audio"`
}

type ParticipantDTO struct {
	SessionID   string `json:"sessionId"`
	UserID      string `json:"userId,omitempty"`
	DisplayName string `json:"displayName"`
	ChannelID   string `json:"channelId"`
	Speaking    bool   `json:"speaking"`
	Muted       bool   `json:"muted"`
	Deafened    bool   `json:"deafened"`
	Away        bool   `json:"away"`
	Local       bool   `json:"local"`
}

type ScreenStreamDTO struct {
	ID             string `json:"id"`
	OwnerSessionID string `json:"ownerSessionId"`
	ChannelID      string `json:"channelId"`
}

type AudioStateDTO struct {
	Muted              bool    `json:"muted"`
	Deafened           bool    `json:"deafened"`
	CaptureAvailable   bool    `json:"captureAvailable"`
	RNNoiseEnabled     bool    `json:"rnnoiseEnabled"`
	RNNoiseSensitivity float32 `json:"rnnoiseSensitivity"`
	MicrophoneGain     float32 `json:"microphoneGain"`
	VADEnabled         bool    `json:"vadEnabled"`
	VADMode            string  `json:"vadMode"`
	VADSensitivity     float32 `json:"vadSensitivity"`
	VADOpen            bool    `json:"vadOpen"`
}

type AudioDeviceDTO struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefault"`
}

type AudioDevicesDTO struct {
	Capture          []AudioDeviceDTO `json:"capture"`
	Playback         []AudioDeviceDTO `json:"playback"`
	SelectedCapture  string           `json:"selectedCapture"`
	SelectedPlayback string           `json:"selectedPlayback"`
}

type AudioMeterDTO struct {
	Input       float32 `json:"input"`
	Processed   float32 `json:"processed"`
	Transmitted float32 `json:"transmitted"`
}

type ConnectionStatsDTO struct {
	Voice                  VoiceStatsDTO `json:"voice"`
	SessionID              string        `json:"sessionId"`
	Generation             string        `json:"generation"`
	Status                 string        `json:"status"`
	PingMS                 float64       `json:"pingMs"`
	PingVariationMS        float64       `json:"pingVariationMs"`
	PingAvailable          bool          `json:"pingAvailable"`
	PingVariationAvailable bool          `json:"pingVariationAvailable"`
	PingSampleAtMS         int64         `json:"pingSampleAtMs"`
	IncomingLoss           float64       `json:"incomingLoss"`
	IncomingKnown          bool          `json:"incomingKnown"`
	IncomingSampleAtMS     int64         `json:"incomingSampleAtMs"`
	OutgoingLoss           float64       `json:"outgoingLoss"`
	OutgoingKnown          bool          `json:"outgoingKnown"`
	RecoveredLoss          float64       `json:"recoveredLoss"`
	ConcealedLoss          float64       `json:"concealedLoss"`
	LossBurstsSingle       int           `json:"lossBurstsSingle"`
	LossBurstsDouble       int           `json:"lossBurstsDouble"`
	LossBurstsLong         int           `json:"lossBurstsLong"`
}

type VoiceDirectionDTO struct {
	Known    bool   `json:"known"`
	Desired  byte   `json:"desired"`
	Allowed  byte   `json:"allowed"`
	Actual   byte   `json:"actual"`
	Reason   byte   `json:"reason"`
	Limited  bool   `json:"limited"`
	Restored uint64 `json:"restored"`
	Late     uint64 `json:"late"`
	PLC      uint64 `json:"plc"`
}
type VoiceStatsDTO struct {
	ReceivedCopies    uint64            `json:"receivedCopies"`
	SizeDrops         uint64            `json:"sizeDrops"`
	ProtocolDrops     uint64            `json:"protocolDrops"`
	Incoming          VoiceDirectionDTO `json:"incoming"`
	Outgoing          VoiceDirectionDTO `json:"outgoing"`
	BudgetDrops       uint64            `json:"budgetDrops"`
	RepairBudgetDrops uint64            `json:"repairBudgetDrops"`
	QueueDrops        uint64            `json:"queueDrops"`
	WriteFailures     uint64            `json:"writeFailures"`
}

type ClientViewDTO struct {
	ChatContext      string            `json:"chatContext"`
	UserID           string            `json:"userId"`
	ChatRevision     string            `json:"chatRevision"`
	ConnectionStatus string            `json:"connectionStatus"`
	LastError        string            `json:"lastError,omitempty"`
	Server           ServerDTO         `json:"server"`
	Revision         string            `json:"revision"`
	SessionID        string            `json:"sessionId"`
	ChannelID        string            `json:"channelId"`
	CanKick          bool              `json:"canKick"`
	CanBan           bool              `json:"canBan"`
	CanDrag          bool              `json:"canDrag"`
	CanAdmin         bool              `json:"canAdmin"`
	IsOwner          bool              `json:"isOwner"`
	JoinLevel        uint16            `json:"joinLevel,omitempty"` // administrators only
	SnapshotFresh    bool              `json:"snapshotFresh"`
	Channels         []ChannelDTO      `json:"channels"`
	Participants     []ParticipantDTO  `json:"participants"`
	ScreenStreams    []ScreenStreamDTO `json:"screenStreams"`
	Audio            AudioStateDTO     `json:"audio"`
	SoundPack        string            `json:"soundPack"`
	SoundVolume      float32           `json:"soundVolume"`
	MuteOnAway       bool              `json:"muteOnAway"`
	DeafenOnAway     bool              `json:"deafenOnAway"`
	SoundCues        map[string]bool   `json:"soundCues"`

	ServerIdentityChange *clientapp.ServerIdentityChange `json:"serverIdentityChange,omitempty"`
}

type ClientEventDTO struct {
	Sequence string `json:"sequence"`
	Time     string `json:"time"`
	Kind     string `json:"kind"`
	Message  string `json:"message"`
	Revision string `json:"revision,omitempty"`
}

func viewDTO(view voiceclient.ClientViewState, lastError string) ClientViewDTO {
	// Numeric access levels reach the UI only for administrators; everyone
	// else sees just whether a channel can be joined.
	admin := view.Permissions&(protocol.PrivilegeAdmin|protocol.PrivilegeOwner) != 0
	levelFor := func(level uint16) uint16 {
		if admin {
			return level
		}
		return 0
	}
	channels := make([]ChannelDTO, 0, len(view.Channels))
	for _, channel := range view.Channels {
		channels = append(channels, ChannelDTO{
			ID:           formatUint64(uint64(channel.ID)),
			ParentID:     formatUint64(uint64(channel.ParentID)),
			Name:         channel.Name,
			Topic:        channel.Topic,
			Description:  channel.Description,
			Position:     channel.Position,
			MaxUsers:     channel.MaxUsers,
			MinJoinLevel: levelFor(channel.MinJoinLevel),
			CanJoin:      view.JoinLevel >= channel.MinJoinLevel,
			Audio:        audioProfileDTO(channel.Audio),
		})
	}
	participants := make([]ParticipantDTO, 0, len(view.Participants))
	for _, participant := range view.Participants {
		muted := participant.Muted
		deafened := participant.Deafened
		if participant.SessionID == view.SessionID {
			muted = view.Muted
			deafened = view.Deafened
		}
		participants = append(participants, ParticipantDTO{
			SessionID:   formatUint64(participant.SessionID),
			UserID:      strconv.FormatInt(participant.UserID, 10),
			DisplayName: participant.DisplayName,
			ChannelID:   formatUint64(uint64(participant.ChannelID)),
			Speaking:    view.Speaking[participant.SessionID],
			Muted:       muted,
			Deafened:    deafened,
			Away:        participant.Away,
			Local:       participant.SessionID == view.SessionID,
		})
	}
	streams := make([]ScreenStreamDTO, 0, len(view.ScreenStreams))
	for _, stream := range view.ScreenStreams {
		streams = append(streams, ScreenStreamDTO{ID: formatUint64(uint64(stream.ID)), OwnerSessionID: formatUint64(stream.OwnerSessionID), ChannelID: formatUint64(uint64(stream.ChannelID))})
	}
	return ClientViewDTO{
		UserID:           strconv.FormatInt(view.UserID, 10),
		ChatRevision:     formatUint64(view.ChatRevision),
		ConnectionStatus: connectionStatus(view.ConnectionStatus),
		LastError:        lastError,
		Server:           ServerDTO{Name: view.ServerInfo.Name, Version: view.ServerInfo.Version, DefaultChannelID: defaultChannelID(view.ServerInfo.DefaultChannelID)},
		Revision:         formatUint64(uint64(view.Revision)),
		SessionID:        formatUint64(view.SessionID),
		ChannelID:        formatUint64(uint64(view.ChannelID)),
		CanKick:          view.Permissions&protocol.PrivilegeKick != 0,
		CanBan:           view.Permissions&protocol.PrivilegeBan != 0,
		CanDrag:          view.Permissions&protocol.PrivilegeDrag != 0,
		CanAdmin:         admin,
		IsOwner:          view.Permissions&protocol.PrivilegeOwner != 0,
		JoinLevel:        levelFor(view.JoinLevel),
		SnapshotFresh:    view.SnapshotFresh,
		Channels:         channels,
		Participants:     participants,
		ScreenStreams:    streams,
		Audio: AudioStateDTO{
			Muted:              view.Muted,
			Deafened:           view.Deafened,
			CaptureAvailable:   view.CaptureAvailable,
			RNNoiseEnabled:     view.RNNoiseEnabled,
			RNNoiseSensitivity: view.RNNoiseSensitivity,
			MicrophoneGain:     view.MicrophoneGain,
			VADEnabled:         view.VADEnabled,
			VADMode:            view.VADMode,
			VADSensitivity:     view.VADSensitivity,
			VADOpen:            view.VADOpen,
		},
		SoundPack:    view.SoundPack,
		SoundVolume:  view.SoundVolume,
		MuteOnAway:   view.MuteOnAway,
		DeafenOnAway: view.DeafenOnAway,
		SoundCues:    view.SoundCues,
	}
}

func eventDTO(event clientapp.ClientEvent) ClientEventDTO {
	dto := ClientEventDTO{
		Sequence: formatUint64(event.Sequence),
		Time:     event.Time.UTC().Format(time.RFC3339Nano),
		Kind:     event.Kind,
		Message:  event.Message,
	}
	if event.Revision != 0 {
		dto.Revision = formatUint64(event.Revision)
	}
	return dto
}

func audioProfileDTO(profile domain.AudioProfile) AudioProfileDTO {
	codec := "unknown"
	if profile.Codec == domain.AudioCodecOpus {
		codec = "opus"
	}
	application := "unknown"
	switch profile.Application {
	case domain.OpusApplicationAudio:
		application = "audio"
	case domain.OpusApplicationVoIP:
		application = "voip"
	}
	return AudioProfileDTO{
		Codec:           codec,
		SampleRate:      profile.SampleRate,
		Channels:        profile.Channels,
		FrameDurationMS: profile.FrameDurationMS,
		Bitrate:         profile.Bitrate,
		Application:     application,
	}
}

func audioDeviceDTOs(devices []audio.DeviceInfo) []AudioDeviceDTO {
	result := make([]AudioDeviceDTO, 0, len(devices))
	for _, device := range devices {
		result = append(result, AudioDeviceDTO{ID: device.ID, Name: device.Name, IsDefault: device.IsDefault})
	}
	return result
}

func connectionStatus(status voiceclient.ConnectionStatus) string {
	switch status {
	case voiceclient.ConnectionConnecting:
		return "connecting"
	case voiceclient.ConnectionConnected:
		return "connected"
	case voiceclient.ConnectionReconnecting:
		return "reconnecting"
	case voiceclient.ConnectionDisconnected:
		return "disconnected"
	default:
		return "unknown"
	}
}

func formatUint64(value uint64) string { return strconv.FormatUint(value, 10) }

func parseUint64(value, field string, allowZero bool) (uint64, error) {
	if value == "" {
		if allowZero {
			return 0, nil
		}
		return 0, &validationError{Field: field, Message: "значение обязательно"}
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || (!allowZero && parsed == 0) {
		return 0, &validationError{Field: field, Message: "некорректное значение"}
	}
	return parsed, nil
}

func defaultChannelID(id domain.ChannelID) string {
	if id == 0 {
		return ""
	}
	return formatUint64(uint64(id))
}
