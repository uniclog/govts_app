package voice

import (
	"net"
	"time"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/voiceflow"
)

type Session struct {
	ID          uint64
	UserID      int64
	JoinLevel   uint16
	Permissions uint8
	Owner       bool
	Name        string
	ChannelID   domain.ChannelID
	Muted       bool
	Deafened    bool
	Away        bool
	// Voice identity is server-owned. These counters survive silence and are
	// independent from transport attempts and the domain event revision.
	SourceEpoch        uint32
	ReceiverEpoch      uint32
	OwnControlRevision uint64
	voiceControl       *voiceControlSession
	voiceFrames        *voiceflow.Window
	voiceFlow          *voiceflow.Endpoint
	// attentionUsed counts reserved wakes in the current burst.
	// attentionStep is the next lock rung. attentionUntil is when the
	// current lock ends; the zero time means there is no lock.
	// attentionAt is the last reserved wake or the last lock update.
	attentionUsed                int
	attentionStep                int
	attentionUntil               time.Time
	attentionAt                  time.Time
	Addr                         *net.UDPAddr
	LastSeen                     time.Time
	MediaCredential              [32]byte
	FileCredential               [32]byte
	FileCredentialExpiry         time.Time
	PreviousFileCredential       [32]byte
	PreviousFileCredentialExpiry time.Time
	// VoiceBundles reports that the client accepts PacketVoiceBundle.
	VoiceBundles  bool
	voiceSeen     map[uint32]struct{}
	voiceArrivals []voiceSample
	// lastVoice is the sender's previous frame kept for server → client
	// redundancy; it lives and dies with the session.
	lastVoice protocol.VoiceBundleFrame
	// voiceSequence and voiceSequenceAt are the newest accepted Sequence and
	// its arrival time; AcceptVoiceSequence bounds forward jumps from them.
	voiceSequence   uint32
	voiceSequenceAt time.Time
}

type voiceSample struct {
	sequence uint32
	at       time.Time
}
