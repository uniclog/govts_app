package protocol

import "errors"

const (
	audioFlagMuted    byte = 1 << 0
	audioFlagDeafened byte = 1 << 1
)

func EncodeAudioState(muted, deafened bool) []byte {
	return []byte{audioStateFlags(muted, deafened)}
}

func DecodeAudioState(payload []byte) (bool, bool, error) {
	if len(payload) != 1 {
		return false, false, errors.New("invalid audio state payload")
	}
	return audioStateFromFlags(payload[0])
}

func audioStateFlags(muted, deafened bool) byte {
	var flags byte
	if muted {
		flags |= audioFlagMuted
	}
	if deafened {
		flags |= audioFlagDeafened
	}
	return flags
}

func audioStateFromFlags(flags byte) (bool, bool, error) {
	if flags&^(audioFlagMuted|audioFlagDeafened) != 0 {
		return false, false, errors.New("invalid audio state flags")
	}
	return flags&audioFlagMuted != 0, flags&audioFlagDeafened != 0, nil
}
