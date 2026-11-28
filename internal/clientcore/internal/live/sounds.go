package client

import (
	"fmt"
	"math"
	"time"

	"uniclog.io/sonoryx/internal/audio"
	"uniclog.io/sonoryx/internal/soundprefs"
)

const (
	SoundPackShort  = soundprefs.SoundPackShort
	SoundPackSoft   = soundprefs.SoundPackSoft
	SoundPackLow    = soundprefs.SoundPackLow
	SoundPackDouble = soundprefs.SoundPackDouble
	SoundPackSignal = soundprefs.SoundPackSignal

	DefaultSoundPack   = soundprefs.DefaultSoundPack
	DefaultSoundVolume = soundprefs.DefaultSoundVolume
)

type soundPack struct {
	id       string
	root     float64
	noteMS   int
	gapMS    int
	harmonic float64
	mix      float64
	peak     float64
}

var soundPacks = []soundPack{
	{id: SoundPackShort, root: 988, noteMS: 40, gapMS: 0, peak: 5600},
	{id: SoundPackSoft, root: 520, noteMS: 60, gapMS: 0, peak: 5200},
	{id: SoundPackLow, root: 280, noteMS: 140, gapMS: 30, harmonic: 2, mix: 0.4, peak: 6400},
	{id: SoundPackDouble, root: 440, noteMS: 70, gapMS: 46, peak: 5600},
	{id: SoundPackSignal, root: 784, noteMS: 100, gapMS: 20, harmonic: 3, mix: 0.25, peak: 6000},
}

func NormalizeSoundPack(id string) (string, bool) {
	for _, pack := range soundPacks {
		if pack.id == id {
			return id, true
		}
	}
	return "", false
}

func packByID(id string) soundPack {
	for _, pack := range soundPacks {
		if pack.id == id {
			return pack
		}
	}
	for _, pack := range soundPacks {
		if pack.id == DefaultSoundPack {
			return pack
		}
	}
	return soundPacks[0]
}

type cueNote struct {
	ratio float64
	ms    int
	amp   float64
	gap   int
}

// ms keeps a cue's rhythm recognizable in every pack. Pitch still follows
// the pack root; only a narrow tempo range is allowed, so a short pack
// does not collapse a melody into the same click as a message.
func (p soundPack) ms(base int) int {
	scale := p.noteMS
	if scale < 45 {
		scale = 45
	}
	if scale > 80 {
		scale = 80
	}
	v := base * scale / 60
	if v < 16 {
		return 16
	}
	return v
}

// gap is a pause a cue already has, widened by half the pack's own gap.
// Legato cues pass 0 directly and stay legato in every pack.
func (p soundPack) gap(base int) int {
	return p.ms(base) + p.gapMS/2
}

func (p soundPack) notes(kind NotificationSoundKind) []cueNote {
	switch kind {
	case NotificationJoined:
		// Major triad, even notes, no gaps. 120 ms on the default pack.
		return []cueNote{{1, p.ms(40), 1, 0}, {1.26, p.ms(40), 1, 0}, {1.5, p.ms(40), 1, 0}}
	case NotificationLeft:
		// Slow fall of a fifth. The second note is held.
		return []cueNote{{1.5, p.ms(70), 0.95, p.gap(36)}, {1, p.ms(150), 0.85, 0}}
	case NotificationChannelChanged:
		// Two staccato notes a fourth apart, with a gap. Not a triad.
		return []cueNote{{1, p.ms(28), 0.9, p.gap(36)}, {1.335, p.ms(28), 0.9, 0}}
	case NotificationMessageSent:
		// A tight high double-tick, much shorter than join or leave.
		return []cueNote{{1.78, p.ms(18), 0.85, p.gap(14)}, {2, p.ms(18), 0.8, 0}}
	case NotificationMessageReceived:
		// Two knocks on one pitch, with a pause between them.
		return []cueNote{{1.12, p.ms(36), 0.6, p.gap(78)}, {1.12, p.ms(52), 0.7, 0}}
	case NotificationScreenStarted:
		// Rising octave.
		return []cueNote{{1, p.ms(48), 1, p.gap(18)}, {2, p.ms(84), 1, 0}}
	case NotificationScreenStopped:
		// Falling octave.
		return []cueNote{{2, p.ms(48), 0.9, p.gap(18)}, {1, p.ms(96), 0.8, 0}}
	case NotificationMicMuted:
		// High, legato, falling. A small switch, no gap.
		return []cueNote{{1.9, p.ms(26), 0.85, 0}, {1.5, p.ms(34), 0.75, 0}}
	case NotificationMicUnmuted:
		// High, legato, rising.
		return []cueNote{{1.4, p.ms(26), 0.8, 0}, {1.9, p.ms(40), 0.9, 0}}
	case NotificationSpeakerMuted:
		// Lower than the microphone, falling fifth, with a gap.
		return []cueNote{{1, p.ms(48), 0.95, p.gap(26)}, {0.67, p.ms(84), 0.85, 0}}
	case NotificationSpeakerUnmuted:
		// Lower than the microphone, rising fifth, with a gap.
		return []cueNote{{0.67, p.ms(48), 0.85, p.gap(26)}, {1, p.ms(84), 0.95, 0}}
	case NotificationAway:
		// Descending minor shape. The last note is the longest.
		return []cueNote{{1.5, p.ms(48), 0.9, p.gap(24)}, {1.19, p.ms(48), 0.85, p.gap(24)}, {0.89, p.ms(110), 0.75, 0}}
	case NotificationReturned:
		// Starts low, jumps, and lands high. The gaps separate it from join.
		return []cueNote{{0.75, p.ms(36), 0.8, p.gap(16)}, {1.26, p.ms(36), 0.9, p.gap(16)}, {1.68, p.ms(72), 1, 0}}
	case NotificationWake:
		// Alternating alarm pulses, louder than the other cues.
		g := p.gap(70)
		w := p.ms(80)
		return []cueNote{{1.6, w, 2.2, g}, {1, w, 2.2, g}, {1.6, w, 2.2, g}, {1, w, 2.2, 0}}
	default:
		return []cueNote{{1, p.ms(60), 1, 0}}
	}
}

func (kind NotificationSoundKind) String() string {
	switch kind {
	case NotificationJoined:
		return "joined"
	case NotificationLeft:
		return "left"
	case NotificationChannelChanged:
		return "channel"
	case NotificationMessageSent:
		return "message_sent"
	case NotificationMessageReceived:
		return "message_received"
	case NotificationScreenStarted:
		return "screen_start"
	case NotificationScreenStopped:
		return "screen_stop"
	case NotificationWake:
		return "wake"
	case NotificationAway:
		return "away"
	case NotificationReturned:
		return "returned"
	case NotificationMicMuted:
		return "mic_off"
	case NotificationMicUnmuted:
		return "mic_on"
	case NotificationSpeakerMuted:
		return "speaker_off"
	case NotificationSpeakerUnmuted:
		return "speaker_on"
	default:
		return fmt.Sprintf("unknown(%d)", uint8(kind))
	}
}

func CueID(kind NotificationSoundKind) (string, bool) {
	switch kind {
	case NotificationMicMuted:
		return soundprefs.CueMicOff, true
	case NotificationMicUnmuted:
		return soundprefs.CueMicOn, true
	case NotificationSpeakerMuted:
		return soundprefs.CueSpeakerOff, true
	case NotificationSpeakerUnmuted:
		return soundprefs.CueSpeakerOn, true
	case NotificationAway:
		return soundprefs.CueAway, true
	case NotificationReturned:
		return soundprefs.CueReturned, true
	case NotificationMessageSent:
		return soundprefs.CueMessageSent, true
	case NotificationScreenStarted:
		return soundprefs.CueScreenStart, true
	case NotificationScreenStopped:
		return soundprefs.CueScreenStop, true
	case NotificationChannelChanged:
		return soundprefs.CueChannel, true
	default:
		return "", false
	}
}

func NotificationCueDuration(pack string, kind NotificationSoundKind) time.Duration {
	notes := packByID(pack).notes(kind)
	total := 0
	for i, note := range notes {
		total += note.ms
		if i+1 < len(notes) {
			total += note.gap
		}
	}
	if total < 0 {
		total = 0
	}
	return time.Duration(total) * time.Millisecond
}

func PreviewSoundKinds() []NotificationSoundKind {
	return []NotificationSoundKind{
		NotificationJoined,
		NotificationLeft,
		NotificationChannelChanged,
		NotificationMessageSent,
		NotificationMessageReceived,
		NotificationScreenStarted,
		NotificationScreenStopped,
		NotificationMicMuted,
		NotificationMicUnmuted,
		NotificationSpeakerMuted,
		NotificationSpeakerUnmuted,
		NotificationAway,
		NotificationReturned,
		NotificationWake,
	}
}

func RenderNotificationPCM(pack string, volume float32, kind NotificationSoundKind) []int16 {
	frames := notificationFrames(NotificationSound{Kind: kind, Pack: pack, Volume: volume})
	total := 0
	for _, frame := range frames {
		total += len(frame.Samples)
	}
	samples := make([]int16, 0, total)
	for _, frame := range frames {
		samples = append(samples, frame.Samples...)
	}
	return samples
}

func notificationFrames(sound NotificationSound) []audio.MediaPCMFrame {
	const (
		sampleRate   = 48000
		frameSamples = sampleRate * 20 / 1000
	)
	pack := packByID(sound.Pack)
	notes := pack.notes(sound.Kind)
	volume := float64(sound.Volume)
	if volume < 0 {
		volume = 0
	}
	if volume > 1 {
		volume = 1
	}
	total := 0
	for i, note := range notes {
		total += note.ms * sampleRate / 1000
		if i+1 < len(notes) {
			total += note.gap * sampleRate / 1000
		}
	}
	if total == 0 {
		return nil
	}
	samples := make([]int16, total)
	offset := 0
	for i, note := range notes {
		count := note.ms * sampleRate / 1000
		if count > 0 {
			hz := pack.root * note.ratio
			peak := pack.peak * note.amp * volume
			for s := 0; s < count; s++ {
				envelope := math.Sin(math.Pi * float64(s) / float64(count))
				wave := math.Sin(2 * math.Pi * hz * float64(s) / sampleRate)
				if pack.harmonic > 1 && pack.mix > 0 {
					wave += pack.mix * math.Sin(2*math.Pi*hz*pack.harmonic*float64(s)/sampleRate)
					wave /= 1 + pack.mix
				}
				samples[offset+s] = clampInt16(int64(wave * envelope * peak))
			}
			offset += count
		}
		if i+1 < len(notes) {
			offset += note.gap * sampleRate / 1000
		}
	}
	frames := make([]audio.MediaPCMFrame, 0, (len(samples)+frameSamples-1)/frameSamples)
	for start := 0; start < len(samples); start += frameSamples {
		chunk := make([]int16, frameSamples)
		copy(chunk, samples[start:min(start+frameSamples, len(samples))])
		frames = append(frames, audio.MediaPCMFrame{PlaybackEpoch: sound.PlaybackEpoch, SenderID: 0, Samples: chunk, Duration: 20 * time.Millisecond})
	}
	if len(frames) > 0 {
		frames[0].Cue = uint8(sound.Kind)
	}
	return frames
}
