package domain

import "fmt"

const (
	MaxChannelNameBytes        = 64
	MaxChannelTopicBytes       = 128
	MaxChannelDescriptionBytes = 512
	MaxParticipantNameBytes    = 64
	MaxServerNameBytes         = 128
	MaxChannelDepth            = 8
)

type ChannelID uint64

type StateRevision uint64

type StreamID uint64

// ScreenStream is channel-visible metadata. Media credentials, SDP and ICE
// candidates deliberately never enter the replicated server snapshot.
type ScreenStream struct {
	ID             StreamID
	OwnerSessionID uint64
	ChannelID      ChannelID
}

type ChannelType uint8

const (
	ChannelTypePermanent ChannelType = iota + 1
)

type AudioCodec uint8

const (
	AudioCodecOpus AudioCodec = iota + 1
)

type OpusApplication uint8

const (
	OpusApplicationAudio OpusApplication = iota + 1
	OpusApplicationVoIP
)

type AudioProfile struct {
	Codec           AudioCodec
	SampleRate      uint32
	Channels        uint8
	FrameDurationMS uint16
	Bitrate         uint32
	Application     OpusApplication
}

func DefaultAudioProfile() AudioProfile {
	return AudioProfile{
		Codec:           AudioCodecOpus,
		SampleRate:      48_000,
		Channels:        1,
		FrameDurationMS: 20,
		Bitrate:         24_000,
		Application:     OpusApplicationVoIP,
	}
}

// ValidateAudioProfile validates the audio settings currently supported by the
// client audio pipeline. Bitrate and Opus application may vary per channel;
// the PCM format remains fixed because RNNoise and WebRTC VAD require it.
func ValidateAudioProfile(profile AudioProfile) error {
	if profile.Codec != AudioCodecOpus {
		return fmt.Errorf("unsupported audio codec %d", profile.Codec)
	}
	if profile.SampleRate != 48_000 || profile.Channels != 1 || profile.FrameDurationMS != 20 {
		return fmt.Errorf("unsupported PCM format %d Hz/%d channels/%d ms", profile.SampleRate, profile.Channels, profile.FrameDurationMS)
	}
	if profile.Bitrate < 6_000 || profile.Bitrate > 510_000 {
		return fmt.Errorf("Opus bitrate %d is outside 6000..510000", profile.Bitrate)
	}
	if profile.Application != OpusApplicationAudio && profile.Application != OpusApplicationVoIP {
		return fmt.Errorf("unsupported Opus application %d", profile.Application)
	}
	return nil
}

type Channel struct {
	ID           ChannelID
	ParentID     ChannelID
	Name         string
	Topic        string
	Description  string
	Position     uint32
	MaxUsers     uint32
	MinJoinLevel uint16
	Type         ChannelType
	Audio        AudioProfile
}

type Participant struct {
	SessionID   uint64
	DisplayName string
	ChannelID   ChannelID
	Muted       bool
	Deafened    bool
}

type ServerInfo struct {
	Name             string
	MediaPort        uint16
	DefaultChannelID ChannelID
}

// ServerSnapshot is the immutable, client-safe view of authoritative server state.
type ServerSnapshot struct {
	Revision      StateRevision
	Info          ServerInfo
	Channels      []Channel
	Participants  []Participant
	ScreenStreams []ScreenStream
}

func (snapshot ServerSnapshot) Clone() ServerSnapshot {
	snapshot.Channels = append([]Channel(nil), snapshot.Channels...)
	snapshot.Participants = append([]Participant(nil), snapshot.Participants...)
	snapshot.ScreenStreams = append([]ScreenStream(nil), snapshot.ScreenStreams...)
	return snapshot
}
