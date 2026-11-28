package client

import (
	"context"
	"math"
	"time"

	"uniclog.io/sonoryx/internal/audio"
)

// At the fixed 20 ms audio profile, keep at most 100 ms per sender.
const maxMixerFramesPerSender = 5

// maxEffectFrames is 1.8 s, enough for the longest wake cue.
const maxEffectFrames = 90

// Some sound cards and their effect processing mute an output that carried
// only digital silence and swallow the first part of a short cue that ends
// it. After cueWakeIdle of silence a cue starts with CueWakePreroll of a
// cueWakeHz tone at cueWakeAmplitude (about -54 dBFS): below hearing at that
// frequency on headphones, yet enough signal to wake the output.
const (
	cueWakeIdle      = 400 * time.Millisecond
	CueWakePreroll   = 360 * time.Millisecond
	cueWakeHz        = 40
	cueWakeAmplitude = 64
)

// cueWakeFrames is the pre-roll before a cue that ends a silent output. It
// fades in and out so the tone itself does not click.
func cueWakeFrames(epoch uint64) []audio.MediaPCMFrame {
	const (
		sampleRate   = 48000
		frameSamples = sampleRate * 20 / 1000
	)
	total := int(CueWakePreroll / (20 * time.Millisecond))
	count := total * frameSamples
	fade := sampleRate * 20 / 1000
	frames := make([]audio.MediaPCMFrame, 0, total)
	for f := 0; f < total; f++ {
		samples := make([]int16, frameSamples)
		for i := range samples {
			s := f*frameSamples + i
			gain := 1.0
			if s < fade {
				gain = float64(s) / float64(fade)
			} else if count-s < fade {
				gain = float64(count-s) / float64(fade)
			}
			samples[i] = int16(cueWakeAmplitude * gain * math.Sin(2*math.Pi*cueWakeHz*float64(s)/sampleRate))
		}
		frames = append(frames, audio.MediaPCMFrame{PlaybackEpoch: epoch, Samples: samples, Duration: 20 * time.Millisecond})
	}
	return frames
}

type pcmMixer struct {
	pruned        voiceMediaStamp
	bySender      map[uint64][]audio.MediaPCMFrame
	droppedFrames uint64
}

func newPCMMixer() *pcmMixer {
	return &pcmMixer{
		bySender: make(map[uint64][]audio.MediaPCMFrame),
	}
}

func (m *pcmMixer) Push(frame audio.MediaPCMFrame) {
	queue := m.bySender[frame.SenderID]
	if len(queue) == maxMixerFramesPerSender {
		copy(queue, queue[1:])
		queue[len(queue)-1] = frame
		m.droppedFrames++
	} else {
		queue = append(queue, frame)
	}
	m.bySender[frame.SenderID] = queue
}

func (m *pcmMixer) Mix() (audio.PCMFrame, bool) {
	frames := make([]audio.MediaPCMFrame, 0, len(m.bySender))
	for senderID, queue := range m.bySender {
		if len(queue) == 0 {
			delete(m.bySender, senderID)
			continue
		}

		frames = append(frames, queue[0])
		queue[0] = audio.MediaPCMFrame{} // Release consumed sample storage.
		if len(queue) == 1 {
			delete(m.bySender, senderID)
		} else {
			m.bySender[senderID] = queue[1:]
		}
	}

	if len(frames) == 0 {
		return audio.PCMFrame{}, false
	}
	return mixPCMFrames(frames), true
}

func mixPCMFrames(frames []audio.MediaPCMFrame) audio.PCMFrame {
	var epoch uint64
	for _, frame := range frames {
		if frame.PlaybackEpoch > epoch {
			epoch = frame.PlaybackEpoch
		}
	}
	sampleCount := 0
	duration := time.Duration(0)
	var cue uint8
	var voices []audio.VoiceReference
	for _, frame := range frames {
		if frame.PlaybackEpoch != epoch {
			if frame.Cue != 0 {
				audio.SoundLogf("sound cue dropped: kind=%s reason=stale_epoch epoch=%d current=%d", NotificationSoundKind(frame.Cue), frame.PlaybackEpoch, epoch)
			}
			continue
		}
		if frame.Voice.Generation != 0 {
			voices = append(voices, audio.VoiceReference{SenderID: frame.SenderID, Identity: frame.Voice})
		}
		if frame.Cue != 0 {
			cue = frame.Cue
		}
		if len(frame.Samples) > sampleCount {
			sampleCount = len(frame.Samples)
		}
		if frame.Duration > duration {
			duration = frame.Duration
		}
	}

	sums := make([]int64, sampleCount)
	for _, frame := range frames {
		if frame.PlaybackEpoch != epoch {
			continue
		}
		for i, sample := range frame.Samples {
			sums[i] += int64(sample)
		}
	}

	samples := make([]int16, sampleCount)
	for i, sum := range sums {
		samples[i] = clampInt16(sum)
	}
	return audio.PCMFrame{Voices: voices, Samples: samples, Duration: duration, PlaybackEpoch: epoch, Cue: cue}
}

func clampInt16(value int64) int16 {
	const (
		minInt16 = -1 << 15
		maxInt16 = 1<<15 - 1
	)

	if value < minInt16 {
		return minInt16
	}
	if value > maxInt16 {
		return maxInt16
	}
	return int16(value)
}

func (m *pcmMixer) Prune(state *State) {
	stamp := state.mediaStamp()
	if stamp == m.pruned {
		return
	}
	m.pruned = stamp
	for id, queue := range m.bySender {
		kept := queue[:0]
		for _, frame := range queue {
			if state.VoiceMediaAllowed(id, frame.Voice) {
				kept = append(kept, frame)
			}
		}
		clear(queue[len(kept):])
		if len(kept) == 0 {
			delete(m.bySender, id)
		} else {
			m.bySender[id] = kept
		}
	}
}

func MixLoopWithState(ctx context.Context, decodedCh <-chan audio.MediaPCMFrame, mixedCh chan<- audio.PCMFrame, interval time.Duration, state *State) error {
	changes, unsubscribe := state.Subscribe(ctx)
	defer unsubscribe()

	defer close(mixedCh)
	if interval <= 0 {
		interval = frameDuration
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	mixer := newPCMMixer()
	effects := make([]audio.MediaPCMFrame, 0, maxEffectFrames)
	notificationCh := state.NotificationSounds()
	// lastAudible is when the mixer last sent a non-silent frame.
	var lastAudible time.Time
	enqueue := func(sound NotificationSound) {
		frames := notificationFrames(sound)
		if len(frames) == 0 || len(frames) > maxEffectFrames {
			audio.SoundLogf("sound cue dropped: kind=%s reason=length frames=%d", sound.Kind, len(frames))
			return
		}
		if idle := time.Since(lastAudible); len(effects) == 0 && idle >= cueWakeIdle {
			if wake := cueWakeFrames(sound.PlaybackEpoch); len(wake)+len(frames) <= maxEffectFrames {
				audio.SoundLogf("sound cue wake preroll: kind=%s idle_ms=%d preroll_ms=%d", sound.Kind, idle.Milliseconds(), CueWakePreroll.Milliseconds())
				frames = append(wake, frames...)
			}
		}
		if len(effects)+len(frames) > maxEffectFrames {
			audio.SoundLogf("sound cue mixer overflow: kind=%s discarded_frames=%d", sound.Kind, len(effects))
			effects = effects[:0]
		}
		audio.SoundLogf("sound cue mixing: kind=%s frames=%d waiting_frames=%d", sound.Kind, len(frames), len(effects))
		effects = append(effects, frames...)
	}
	drainNotifications := func() {
		for {
			select {
			case sound := <-notificationCh:
				enqueue(sound)
			default:
				return
			}
		}
	}
	// One mixed frame per tick. A cue longer than 20 ms spreads across later
	// ticks and stays on the same clock as voice.
	mixOnce := func() error {
		drainNotifications()
		mixer.Prune(state)
		if len(effects) > 0 {
			mixer.Push(effects[0])
			effects = effects[1:]
		}
		frame, ok := mixer.Mix()
		if !ok {
			return nil
		}
		if activePCM(frame.Samples) {
			lastAudible = time.Now()
		}
		return sendMixedFrame(ctx, mixedCh, frame)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changes:
			mixer.Prune(state)
		case frame, ok := <-decodedCh:
			if !ok {
				mixer.Prune(state)
				return drainMixer(ctx, mixer, mixedCh)
			}
			state.mu.RLock()
			allowed := state.voiceMediaAllowedLocked(frame.SenderID, frame.Voice)
			if allowed {
				mixer.Push(frame)
			}
			state.mu.RUnlock()
			if !allowed {
				continue
			}
			select {
			case <-ticker.C:
				if err := mixOnce(); err != nil {
					return err
				}
			default:
			}
		case sound := <-notificationCh:
			enqueue(sound)
		case <-ticker.C:
			if err := mixOnce(); err != nil {
				return err
			}
		}
	}
}

func drainMixer(
	ctx context.Context,
	mixer *pcmMixer,
	mixedCh chan<- audio.PCMFrame,
) error {
	for {
		frame, ok := mixer.Mix()
		if !ok {
			return nil
		}
		if err := sendMixedFrame(ctx, mixedCh, frame); err != nil {
			return err
		}
	}
}

func sendMixedFrame(
	ctx context.Context,
	mixedCh chan<- audio.PCMFrame,
	frame audio.PCMFrame,
) error {
	select {
	case mixedCh <- frame:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
