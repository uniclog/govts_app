package voice

import (
	"net"
	"time"

	"uniclog.io/govts/internal/domain"
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
	voiceSeen       map[uint32]struct{}
	voiceArrivals   []voiceSample
}

type voiceSample struct {
	sequence uint32
	at       time.Time
}
