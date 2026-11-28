package voice

import (
	"time"

	"uniclog.io/sonoryx/internal/protocol"
)

// Bounds apply to encrypted UDP bytes, independently of audio redundancy
// budgets. The recipient burst fits the largest allowed full snapshot (4096
// sources / 256 pages), so a large assembly cannot wait forever for tokens.
const (
	voiceStateGlobalBytesPerSecond    = 4 * 1024 * 1024
	voiceStateGlobalBurstBytes        = 1024 * 1024
	voiceStateRecipientBytesPerSecond = 64 * 1024
	voiceStateRecipientBurstBytes     = 128 * 1024
	voiceStatePacketOverhead          = protocol.MaxWireDatagramSize - protocol.MaxPayloadSize
)

type voiceStateByteBudget struct {
	at     time.Time
	tokens float64
}

func (b *voiceStateByteBudget) canSpend(now time.Time, size, rate, burst int) bool {
	if b.at.IsZero() {
		b.at, b.tokens = now, float64(burst)
	} else if now.After(b.at) {
		b.tokens = min(float64(burst), b.tokens+now.Sub(b.at).Seconds()*float64(rate))
		b.at = now
	}
	return size >= 0 && size <= burst && b.tokens >= float64(size)
}

func (b *voiceStateByteBudget) spend(size int) { b.tokens -= float64(size) }

func voiceStateSnapshotWireSize(sourceCount int) int {
	pages := max(1, (sourceCount+protocol.MaxVoiceStateEntries-1)/protocol.MaxVoiceStateEntries)
	return sourceCount*protocol.VoiceStateSourceSize + pages*(protocol.VoiceStateHeaderSize+voiceStatePacketOverhead)
}
