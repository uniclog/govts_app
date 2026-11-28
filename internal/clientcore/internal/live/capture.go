package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"time"

	"uniclog.io/sonoryx/internal/audio"
	"uniclog.io/sonoryx/internal/audio/vad"
	"uniclog.io/sonoryx/internal/audio/voicegate"
	"uniclog.io/sonoryx/internal/logging"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
	"uniclog.io/sonoryx/internal/voiceflow"
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
	invalid := logging.NewFailures("audio_encode")
	defer invalid.Close()
	var localSequence uint32
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

			// While the user monitors themselves nothing is transmitted, so
			// the own speaking indicator stays off as others see it.
			if len(states) > 0 && len(frames) > 0 && states[0].Audio.Monitor() == nil {
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
					if monitor := states[0].Audio.Monitor(); monitor != nil {
						monitor(released.Samples)
						continue
					}
				}
				identity := audio.VoiceIdentity{}
				localSequence++
				sequence := localSequence
				if len(states) > 0 {
					var allowed bool
					identity, sequence, allowed = states[0].reserveVoiceFrame(released.ControlEpoch)
					if !allowed {
						continue
					}
				}
				createdAt := time.Now()
				buffer, err := encoder.Encode(released.Samples)
				if err != nil {
					invalid.Record(err, sequence)
					continue
				}
				if len(buffer) == 0 || len(buffer) > protocol.MaxVoiceFrameSize {
					invalid.Record(protocol.ErrInvalidVoiceBundle, sequence)
					continue
				}
				frame := audio.Frame{Voice: identity, Sequence: sequence, CreatedAt: createdAt, Data: buffer, Duration: released.Duration, ControlEpoch: released.ControlEpoch}
				select {
				case audioCh <- frame:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
		}
	}
}

func SendLoopWithStats(ctx context.Context, conn *udp.ClientPacketConn, sessionID uint64, audioCh <-chan audio.Frame, control *AudioControlState, state *State) error {
	return sendLoop(ctx, conn, sessionID, audioCh, control, state)
}

func sendLoop(ctx context.Context, conn *udp.ClientPacketConn, sessionID uint64, audioCh <-chan audio.Frame, control *AudioControlState, state *State) error {
	var datagram uint32
	var history voiceflow.Window
	var identity audio.VoiceIdentity
	invalid := logging.NewFailures("voice_send")
	defer invalid.Close()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case frame, ok := <-audioCh:
			if !ok {
				return nil
			}
			send := func() error {
				if frame.Voice.SourceEpoch == 0 {
					return nil
				}

				if state != nil {
					// Keep the session/epoch stable through the write and accounting.
					state.mu.RLock()
					defer state.mu.RUnlock()
					if !state.outgoingVoiceAllowedLocked(frame.Voice) {
						return nil
					}
				}
				if !frame.CreatedAt.IsZero() && time.Since(frame.CreatedAt) > 40*time.Millisecond {
					if state != nil {
						state.voiceFlow.QueueDrop(time.Now())
					}
					return nil
				}
				if identity != frame.Voice {
					identity = frame.Voice
					history = voiceflow.Window{}
				}
				now := time.Now()
				primary := protocol.VoiceBundleFrame{Sequence: frame.Sequence, Payload: frame.Data}
				if _, err := history.Accept([]protocol.VoiceBundleFrame{primary}, now); err != nil {
					return err
				}
				count := 1
				if state != nil {
					state.voiceFlow.SetEnabled(state.voice.header.EnabledDirections&protocol.VoiceDirectionUp != 0)
					count = state.voiceFlow.FrameCount(now)
				}
				if state != nil {
					state.voiceFlow.SizeDrops(history.SizeDrops(primary, count, now))
				}
				frames := history.ForFrame(primary, count, now)
				if state != nil {
					frames = state.voiceFlow.Select(frames, true, now)
				}

				datagram++ // An attempted local write consumes a transport sequence too.
				packet, err := protocol.NewVoiceBundleV2Packet(sessionID, protocol.VoiceBundleV2{DatagramSequence: datagram, SourceEpoch: identity.SourceEpoch, Frames: frames}, true)
				if err != nil {
					return err
				}
				notAfter := time.Time{}
				if !frame.CreatedAt.IsZero() {
					notAfter = frame.CreatedAt.Add(40 * time.Millisecond)
				}
				err = conn.SendPacketBefore(packet, notAfter)
				if errors.Is(err, udp.ErrSendExpired) && state != nil {
					state.voiceFlow.QueueDrop(time.Now())
				}
				if state != nil {
					state.voiceFlow.Sent(datagram, err == nil, true, time.Now())
				}
				if err != nil {
					return err
				}
				if state != nil {
					state.RecordVoiceSent(datagram)
				}
				return nil
			}
			var err error
			if control != nil {
				err = control.Send(frame.ControlEpoch, send)
			} else {
				err = send()
			}
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return err
				}
				invalid.Record(err, sessionID)
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
