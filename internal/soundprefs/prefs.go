package soundprefs

// Notification sound IDs are shared by the renderer and saved preferences.
const (
	SoundPackShort  = "short"
	SoundPackSoft   = "soft"
	SoundPackLow    = "low"
	SoundPackDouble = "double"
	SoundPackSignal = "signal"

	DefaultSoundPack   = SoundPackSoft
	DefaultSoundVolume = float32(0.8)

	// Button cues. The client maps each one to a notification kind.
	// Absent in SoundCues means the cue is on.
	CueMicOff      = "mic-off"
	CueMicOn       = "mic-on"
	CueSpeakerOff  = "speaker-off"
	CueSpeakerOn   = "speaker-on"
	CueAway        = "away"
	CueReturned    = "returned"
	CueMessageSent = "message-sent"
	CueScreenStart = "screen-start"
	CueScreenStop  = "screen-stop"
	CueChannel     = "channel"
)

// ButtonCues is the stable order of sounds a button can play.
var ButtonCues = []string{
	CueMicOff,
	CueMicOn,
	CueSpeakerOff,
	CueSpeakerOn,
	CueAway,
	CueReturned,
	CueMessageSent,
	CueScreenStart,
	CueScreenStop,
	CueChannel,
}

func KnownCue(id string) bool {
	for _, cue := range ButtonCues {
		if cue == id {
			return true
		}
	}
	return false
}

// SoundCueEnabled reports whether a button cue should play.
// An unknown id is not a button cue and stays on.
func SoundCueEnabled(cues map[string]bool, id string) bool {
	if id == "" || !KnownCue(id) {
		return true
	}
	if cues == nil {
		return true
	}
	enabled, ok := cues[id]
	return !ok || enabled
}

// NormalizeSoundCues keeps only button cues that are explicitly off.
func NormalizeSoundCues(cues map[string]bool) map[string]bool {
	if len(cues) == 0 {
		return nil
	}
	out := make(map[string]bool)
	for _, id := range ButtonCues {
		if enabled, ok := cues[id]; ok && !enabled {
			out[id] = false
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SoundCueStates is every button cue, on unless the stored map turns it off.
func SoundCueStates(cues map[string]bool) map[string]bool {
	out := make(map[string]bool, len(ButtonCues))
	for _, id := range ButtonCues {
		out[id] = SoundCueEnabled(cues, id)
	}
	return out
}

func NormalizeSoundPack(value string) string {
	switch value {
	case SoundPackShort, SoundPackSoft, SoundPackLow, SoundPackDouble, SoundPackSignal:
		return value
	default:
		return DefaultSoundPack
	}
}
