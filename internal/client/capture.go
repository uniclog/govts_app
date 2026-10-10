package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"time"

	"uniclog.io/govts/internal/audio"
	"uniclog.io/govts/internal/audio/vad"
	"uniclog.io/govts/internal/audio/voicegate"
	"uniclog.io/govts/internal/protocol"
	"uniclog.io/govts/internal/transport/udp"
)

const frameDuration = 20 * time.Millisecond

const meterFloorDBFS float32 = -60

func normalizedMeterLevel(levelDBFS float32) float32 {
	if levelDBFS <= meterFloorDBFS {
		return 0
	}
	if levelDBFS >= 0 {
		return 1
	}
	return (levelDBFS - meterFloorDBFS) / -meterFloorDBFS
}

func EncodeLoop(
	ctx context.Context,
	encoder audio.Encoder,
	pcmCh <-chan audio.PCMFrame,
	audioCh chan<- audio.Frame,
	states ...*State,
) error {
	return EncodeLoopWithPipeline(ctx, encoder, nil, nil, nil, pcmCh, audioCh, states...)
}

// EncodeLoopWithPipeline applies filtering, voice detection, gate policy and
// encoding in order. Nil audio stages preserve the legacy passthrough path.
func EncodeLoopWithPipeline(
	ctx context.Context,
	encoder audio.Encoder,
	filter audio.PCMFilter,
	detector vad.Detector,
	gate *voicegate.Gate,
	pcmCh <-chan audio.PCMFrame,
	audioCh chan<- audio.Frame,
	states ...*State,
) error {
	defer close(audioCh)
	var pipelineEpoch uint64
	var havePipelineEpoch bool
	var vadEnabled bool
	var haveVADSetting bool
	if len(states) > 0 {
		defer func() {
			states[0].Audio.SetVADOpen(false)
			states[0].Audio.SetAudioMeter(0, 0, 0)
		}()
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case pcmFrame, ok := <-pcmCh:
			if !ok {
				return nil
			}
			if len(states) > 0 {
				muted, _, epoch := states[0].Audio.Snapshot()
				if muted || epoch != pcmFrame.ControlEpoch {
					continue
				}
			}
			rawLevel := normalizedMeterLevel(vad.LevelDBFS(pcmFrame.Samples))
			if havePipelineEpoch && pipelineEpoch != pcmFrame.ControlEpoch {
				if filter != nil {
					if err := filter.Reset(); err != nil {
						return fmt.Errorf("reset microphone filter: %w", err)
					}
				}
				if detector != nil {
					if err := detector.Reset(); err != nil {
						return fmt.Errorf("reset microphone VAD: %w", err)
					}
				}
				if gate != nil {
					gate.Reset()
				}
			}
			pipelineEpoch = pcmFrame.ControlEpoch
			havePipelineEpoch = true

			if filter != nil {
				if err := filter.Process(pcmFrame.Samples); err != nil {
					return fmt.Errorf("filter microphone PCM: %w", err)
				}
			}
			processedLevel := normalizedMeterLevel(vad.LevelDBFS(pcmFrame.Samples))

			frames := []audio.PCMFrame{pcmFrame}
			var result vad.Result
			var gateConfig voicegate.Config
			if gate != nil {
				gateConfig = voicegate.DefaultConfig()
				if len(states) > 0 {
					settings := states[0].Audio.VADSnapshot()
					gateConfig = settings.GateConfig()
				}
				if err := gate.Configure(gateConfig); err != nil {
					return fmt.Errorf("configure microphone voice gate: %w", err)
				}
				currentVADEnabled := gateConfig.Mode != voicegate.ModeDisabled
				if haveVADSetting && currentVADEnabled != vadEnabled && detector != nil {
					if err := detector.Reset(); err != nil {
						return fmt.Errorf("reset microphone VAD: %w", err)
					}
				}
				vadEnabled = currentVADEnabled
				haveVADSetting = true
				if currentVADEnabled {
					if detector == nil {
						return errors.New("microphone VAD is enabled without a detector")
					}
					var err error
					result, err = detector.Analyze(pcmFrame.Samples)
					if err != nil {
						return fmt.Errorf("analyze microphone PCM: %w", err)
					}
				}
				frames = gate.Process(pcmFrame, result)
				if len(states) > 0 {
					states[0].Audio.SetVADOpen(gate.IsOpen())
				}
			}

			if len(states) > 0 && len(frames) > 0 {
				now := time.Now()
				if gate != nil && gateConfig.Mode != voicegate.ModeDisabled {
					states[0].ObserveVoiceActivity(states[0].SessionID(), voicegate.Active(gateConfig, result), now)
				} else {
					states[0].ObserveSpeaking(states[0].SessionID(), pcmFrame.Samples, now)
				}
			}
			if len(states) > 0 {
				transmittedLevel := float32(0)
				if len(frames) > 0 {
					transmittedLevel = processedLevel
				}
				states[0].Audio.SetAudioMeter(rawLevel, processedLevel, transmittedLevel)
			}

			for _, released := range frames {
				if len(states) > 0 {
					muted, _, epoch := states[0].Audio.Snapshot()
					if muted || epoch != released.ControlEpoch {
						continue
					}
				}
				buffer, err := encoder.Encode(released.Samples)
				if err != nil {
					return err
				}
				frame := audio.Frame{Data: buffer, Duration: released.Duration, ControlEpoch: released.ControlEpoch}
				select {
				case audioCh <- frame:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
}

func SendLoop(
	ctx context.Context,
	conn *udp.ClientPacketConn,
	sessionID uint64,
	audioCh <-chan audio.Frame,
	controls ...*AudioControlState,
) error {
	var control *AudioControlState
	if len(controls) > 0 {
		control = controls[0]
	}
	return sendLoop(ctx, conn, sessionID, audioCh, control, nil)
}

func SendLoopWithStats(ctx context.Context, conn *udp.ClientPacketConn, sessionID uint64, audioCh <-chan audio.Frame, control *AudioControlState, state *State) error {
	return sendLoop(ctx, conn, sessionID, audioCh, control, state)
}

func sendLoop(ctx context.Context, conn *udp.ClientPacketConn, sessionID uint64, audioCh <-chan audio.Frame, control *AudioControlState, state *State) error {
	sequence := uint32(1)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case frame, ok := <-audioCh:
			if !ok {
				return nil
			}
			sent := protocol.NewVoicePacket(sessionID, sequence, frame.Data)
			packetSent := false
			send := func() error {
				if err := conn.SendPacket(sent); err != nil {
					return err
				}
				packetSent = true
				return nil
			}
			var err error
			if control != nil {
				err = control.Send(frame.ControlEpoch, send)
			} else {
				err = send()
			}
			if err != nil {
				return err
			}
			if packetSent {
				if state != nil {
					state.RecordVoiceSent(sequence)
				}
				sequence++
			}
		}
	}
}

func RecordLoop(
	ctx context.Context,
	recorder audio.Recorder,
	pcmCh chan<- audio.PCMFrame,
	samplesPerFrame int,
	controls ...*AudioControlState,
) error {
	defer close(pcmCh)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		var epoch uint64
		if len(controls) > 0 {
			_, _, epoch = controls[0].Snapshot()
		}
		samples := make([]int16, samplesPerFrame)
		n, err := recorder.Read(samples)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		frame := audio.PCMFrame{
			ControlEpoch: epoch,
			Samples:      samples[:n],
			Duration:     frameDuration,
		}
		if len(controls) > 0 {
			applyGain(frame.Samples, controls[0].MicrophoneGain())
			muted, _, current := controls[0].Snapshot()
			if muted || current != epoch {
				controls[0].SetAudioMeter(normalizedMeterLevel(vad.LevelDBFS(frame.Samples)), 0, 0)
				continue
			}
		}
		select {
		case pcmCh <- frame:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// applyGain scales PCM in place and saturates instead of wrapping on overflow.
func applyGain(samples []int16, gain float32) {
	if gain == 1 {
		return
	}
	for i, sample := range samples {
		v := float32(sample) * gain
		if v > math.MaxInt16 {
			v = math.MaxInt16
		} else if v < math.MinInt16 {
			v = math.MinInt16
		}
		samples[i] = int16(v)
	}
}
