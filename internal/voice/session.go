package voice

import (
	"net"
	"time"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

type Session struct {
	ID              uint64
	UserID          int64
	JoinLevel       uint16
	Permissions     uint8
	Owner           bool
	Name            string
	ChannelID       domain.ChannelID
	Muted           bool
	Deafened        bool
	Addr            *net.UDPAddr
	LastSeen        time.Time
	MediaCredential [32]byte
	// VoiceBundles reports that the client accepts PacketVoiceBundle.
	VoiceBundles  bool
	voiceSeen     map[uint32]struct{}
	voiceArrivals []voiceSample
	// lastVoice is the sender's previous frame kept for server → client
	// redundancy; it lives and dies with the session.
	lastVoice protocol.VoiceBundleFrame
}

type voiceSample struct {
	sequence uint32
	at       time.Time
}
