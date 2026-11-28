package webrtcaudio

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/pion/opus"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
	"uniclog.io/sonoryx/internal/audio"
)

func (b *Bridge) receive(id uint64, track *webrtc.TrackRemote, counters *Counters) {
	decoder, err := opus.NewDecoderWithOutput(sampleRate, 1)
	if err != nil {
		counters.Failures.Add(1)
		return
	}
	var scratch [sampleRate * 120 / 1000]int16
	var pending frame
	var filled int
	var lastSequence uint16
	var lastTimestamp uint32
	var lastSamples int
	var epoch uint64
	var started bool
	for {
		packet, _, err := track.ReadRTP()
		if err != nil {
			return
		}
		now := time.Now()
		if started && int16(packet.SequenceNumber-lastSequence) <= 0 {
			continue
		}
		counters.RTPReceived.Add(1)
		if started {
			gap := packet.SequenceNumber - lastSequence - 1
			// A huge jump signals a discontinuity, so it cannot dominate loss totals.
			if gap <= 1000 {
				counters.RTPMissing.Add(uint64(gap))
			}
		}
		current, allowed := b.captureEpoch(id)
		discontinuity := !started || current != epoch || packet.SequenceNumber != lastSequence+1 ||
			packet.Timestamp != lastTimestamp+uint32(lastSamples) || (filled != 0 && now.Sub(pending.at) > maxAge)
		lastSequence, lastTimestamp = packet.SequenceNumber, packet.Timestamp
		epoch, started = current, true
		if !allowed {
			filled, lastSamples = 0, 0
			continue
		}
		if discontinuity {
			filled = 0
			decoder, err = opus.NewDecoderWithOutput(sampleRate, 1)
			if err != nil {
				return
			}
		}
		n, err := decoder.DecodeToInt16(packet.Payload, scratch[:])
		if err != nil || n <= 0 || n > len(scratch) {
			filled, lastSamples = 0, 0
			continue
		}
		lastSamples = n
		for samples := scratch[:n]; len(samples) > 0; {
			if filled == 0 {
				pending.at = now
			}
			written := copy(pending.samples[filled:], samples)
			filled += written
			samples = samples[written:]
			if filled == frameSamples {
				b.push(id, epoch, pending)
				filled = 0
			}
		}
	}
}

type output struct {
	bridge    *Bridge
	id        uint64
	track     *webrtc.TrackLocalStaticRTP
	counters  *Counters
	encoder   *audio.OpusEncoder
	epoch     uint64
	sequence  uint16
	timestamp uint32
}

func newEncoder() (*audio.OpusEncoder, error) {
	cfg := audio.DefaultCodecConfig()
	return audio.NewOpusEncoder(cfg)
}
func newOutput(b *Bridge, id uint64, track *webrtc.TrackLocalStaticRTP, counters *Counters) (*output, error) {
	encoder, err := newEncoder()
	if err != nil {
		return nil, err
	}
	return &output{bridge: b, id: id, track: track, counters: counters, encoder: encoder, sequence: uint16(rand.Uint32()), timestamp: rand.Uint32()}, nil
}
func (o *output) run(ctx context.Context) {
	clock := time.NewTicker(frameTime)
	defer clock.Stop()
	last := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-clock.C:
			steps := max(1, (now.Sub(last)+frameTime/2)/frameTime)
			o.timestamp += uint32(steps) * frameSamples
			last = now
			o.send()
		}
	}
}
func (o *output) send() {
	b := o.bridge
	b.sinkMu.Lock()
	defer b.sinkMu.Unlock()
	b.mu.Lock()
	if b.peer != o.id {
		b.mu.Unlock()
		return
	}
	pcm, hasVoice := b.playback.take(time.Now())
	epoch := b.playbackEpoch
	b.mu.Unlock()
	if epoch != o.epoch {
		encoder, err := newEncoder()
		if err != nil {
			return
		}
		o.encoder, o.epoch = encoder, epoch
	}
	// Opus silence keeps the negotiated downlink alive in listen-only mode.
	data := []byte{0xf8, 0xff, 0xfe}
	if hasVoice {
		payload, err := o.encoder.Encode(pcm.samples[:])
		if err != nil {
			return
		}
		data = payload
		o.counters.PCMDelayMicros.Add(uint64(time.Since(pcm.at).Microseconds()))
		o.counters.PCMSamples.Add(1)
	}
	packet := rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: o.sequence, Timestamp: o.timestamp}, Payload: data}
	o.sequence++
	if err := o.track.WriteRTP(&packet); err != nil {
		o.counters.Failures.Add(1)
	}
}
