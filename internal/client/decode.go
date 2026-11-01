package client

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"uniclog.io/sonoryx/internal/audio"
	"uniclog.io/sonoryx/internal/logging"
)

type DecoderFactory func() (audio.Decoder, error)

var (
	errInvalidAudioFrame = errors.New("invalid audio frame")
	// errLossNotConcealed means a Missing frame has no decoder state to
	// extrapolate from and is skipped.
	errLossNotConcealed = errors.New("lost audio frame not concealed")
)

type streamDecoder struct {
	identity audio.VoiceIdentity
	decoder  audio.Decoder
	lastSeen time.Time
}

type streamDecoders struct {
	pruned     voiceMediaStamp
	newDecoder DecoderFactory
	bySender   map[uint64]streamDecoder
}

func newStreamDecoders(newDecoder DecoderFactory) *streamDecoders {
	return &streamDecoders{
		newDecoder: newDecoder,
		bySender:   make(map[uint64]streamDecoder),
	}
}

func (d *streamDecoders) decode(frame audio.MediaFrame) (audio.MediaPCMFrame, error) {
	return d.decodeAt(frame, time.Now())
}

func (d *streamDecoders) decodeAt(
	frame audio.MediaFrame,
	now time.Time,
) (audio.MediaPCMFrame, error) {
	if stream, ok := d.bySender[frame.SenderID]; ok && stream.identity != frame.Voice {
		delete(d.bySender, frame.SenderID)
	}
	if frame.Missing {
		return d.concealAt(frame, now)
	}

	stream, ok := d.bySender[frame.SenderID]
	if !ok {
		decoder, err := d.newDecoder()
		if err != nil {
			return audio.MediaPCMFrame{}, fmt.Errorf(
				"create decoder for sender %d: %w",
				frame.SenderID,
				err,
			)
		}
		stream.decoder = decoder
		stream.identity = frame.Voice
	}
	stream.lastSeen = now
	d.bySender[frame.SenderID] = stream

	samples, err := stream.decoder.Decode(frame.Data)
	if err != nil {
		// A malformed frame may leave codec state partially updated. Reset only
		// this sender; the next frame will get a fresh decoder.
		delete(d.bySender, frame.SenderID)
		return audio.MediaPCMFrame{}, fmt.Errorf(
			"%w: decode frame from sender %d: %w",
			errInvalidAudioFrame,
			frame.SenderID,
			err,
		)
	}

	return audio.MediaPCMFrame{
		Voice:    frame.Voice,
		SenderID: frame.SenderID,
		Sequence: frame.Sequence,
		Samples:  samples,
		Duration: frame.Duration,
	}, nil
}

// concealAt synthesizes a lost frame with the sender's existing decoder so the
// next real frame continues from the concealed codec state.
func (d *streamDecoders) concealAt(
	frame audio.MediaFrame,
	now time.Time,
) (audio.MediaPCMFrame, error) {
	stream, ok := d.bySender[frame.SenderID]
	if !ok {
		return audio.MediaPCMFrame{}, errLossNotConcealed
	}
	concealer, ok := stream.decoder.(audio.LossConcealer)
	if !ok {
		return audio.MediaPCMFrame{}, errLossNotConcealed
	}
	stream.lastSeen = now
	d.bySender[frame.SenderID] = stream

	samples, err := concealer.ConcealLoss()
	if err != nil {
		delete(d.bySender, frame.SenderID)
		return audio.MediaPCMFrame{}, fmt.Errorf(
			"%w: conceal frame from sender %d: %w",
			errInvalidAudioFrame,
			frame.SenderID,
			err,
		)
	}

	return audio.MediaPCMFrame{
		Voice:    frame.Voice,
		SenderID: frame.SenderID,
		Sequence: frame.Sequence,
		Samples:  samples,
		Duration: frame.Duration,
	}, nil
}

func (d *streamDecoders) RemoveInactive(now time.Time, timeout time.Duration) int {
	removed := 0
	for senderID, stream := range d.bySender {
		if now.Sub(stream.lastSeen) < timeout {
			continue
		}
		delete(d.bySender, senderID)
		removed++
	}
	return removed
}

func (d *streamDecoders) Prune(state *State) {
	if state == nil {
		return
	}
	stamp := state.mediaStamp()
	if stamp == d.pruned {
		return
	}
	d.pruned = stamp
	for id, stream := range d.bySender {
		if !state.VoiceMediaAllowed(id, stream.identity) {
			delete(d.bySender, id)
		}
	}
}

func DecodeLoop(
	ctx context.Context,
	newDecoder DecoderFactory,
	encodedCh <-chan audio.MediaFrame,
	pcmOutCh chan<- audio.MediaPCMFrame,
	states ...*State,
) error {
	defer close(pcmOutCh)
	invalid := logging.NewFailures("audio_decode")
	defer invalid.Close()
	decoders := newStreamDecoders(newDecoder)
	var state *State
	var changes <-chan struct{}
	if len(states) > 0 {
		state = states[0]
		var unsubscribe func()
		changes, unsubscribe = state.Subscribe(ctx)
		defer unsubscribe()
	}
	cleanupTicker := time.NewTicker(streamCleanupInterval)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changes:
			decoders.Prune(state)
		case now := <-cleanupTicker.C:
			decoders.Prune(state)
			decoders.RemoveInactive(now, DefaultStreamIdleTimeout)

		case frame, ok := <-encodedCh:
			if !ok {
				return nil
			}

			decoders.Prune(state)
			if state != nil && !state.VoiceMediaAllowed(frame.SenderID, frame.Voice) {
				continue
			}
			pcmFrame, err := decoders.decode(frame)
			if errors.Is(err, errLossNotConcealed) {
				continue
			}
			if errors.Is(err, errInvalidAudioFrame) {
				invalid.Record(err, frame.SenderID)
				continue
			}
			if err != nil {
				return err
			}
			if state != nil && !state.VoiceMediaAllowed(frame.SenderID, frame.Voice) {
				delete(decoders.bySender, frame.SenderID)
				continue
			}
			if frame.Missing && len(states) > 0 {
				states[0].RecordVoiceConcealed()
				states[0].VoiceEndpoint().Frame(frame.DatagramSequence, 2, 1)
			}
			if len(states) > 0 && !states[0].ObserveSpeaking(frame.SenderID, pcmFrame.Samples, time.Now()) {
				continue
			}
			if len(states) > 0 && !states[0].ParticipantVolumeReady(frame.SenderID) {
				continue
			}
			if len(states) > 0 {
				deafened, epoch := states[0].Audio.PlaybackSnapshot()
				if deafened {
					continue
				}
				pcmFrame.PlaybackEpoch = epoch
				pcmFrame.Samples = scalePCM(pcmFrame.Samples, states[0].Audio.ParticipantVolume(frame.SenderID))
			}

			select {
			case pcmOutCh <- pcmFrame:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func scalePCM(samples []int16, gain float32) []int16 {
	if gain == DefaultParticipantVolume {
		return samples
	}
	result := make([]int16, len(samples))
	for i, sample := range samples {
		result[i] = clampInt16(int64(math.Round(float64(sample) * float64(gain))))
	}
	return result
}
