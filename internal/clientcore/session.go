package clientcore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"uniclog.io/sonoryx/internal/audio"
	audiornnoise "uniclog.io/sonoryx/internal/audio/rnnoise"
	audiovad "uniclog.io/sonoryx/internal/audio/vad"
	"uniclog.io/sonoryx/internal/audio/voicegate"
	voiceclient "uniclog.io/sonoryx/internal/clientcore/internal/live"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

func runSession(parent context.Context, conn *udp.ClientPacketConn, state *voiceclient.State, backend AudioBackend, name string, noticeOutput io.Writer, logger *log.Logger, firstConnection bool, previousChannel domain.ChannelID, onReady func(), onStop ...func()) (runErr error) {
	stopSession := func() {
		for _, stop := range onStop {
			stop()
		}
	}
	defer stopSession()
	if backend == nil {
		return errAudioBackendRequired
	}
	streams := backend.NewSession()
	noticeOutput = &lockedOutput{writer: noticeOutput}
	sessionID := state.SessionID()
	oldSnapshot := state.Snapshot()
	audioCh := make(chan audio.Frame)
	pcmCh := make(chan audio.PCMFrame)
	encodedInCh := make(chan audio.MediaFrame)
	orderedInCh := make(chan audio.MediaFrame)
	controlCh := make(chan protocol.VoicePacket, 16)
	decodedCh := make(chan audio.MediaPCMFrame)
	pcmOutCh := make(chan audio.PCMFrame)
	codecConfig := audio.DefaultCodecConfig()
	encoder, err := audio.NewOpusEncoder(codecConfig)
	if err != nil {
		return fmt.Errorf("create Opus encoder: %w", err)
	}
	filter, err := newMicrophoneFilter(codecConfig, state.Audio)
	if err != nil {
		return err
	}
	defer func() {
		if err := filter.Close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close microphone filter: %w", err))
		}
	}()
	detector, err := newMicrophoneVAD(codecConfig)
	if err != nil {
		return err
	}
	defer func() {
		if err := detector.Close(); err != nil {
			runErr = errors.Join(runErr, fmt.Errorf("close microphone VAD: %w", err))
		}
	}()
	gate, err := voicegate.New(state.Audio.VADSnapshot().GateConfig())
	if err != nil {
		return fmt.Errorf("create microphone voice gate: %w", err)
	}
	player, err := streams.OpenPlayer(codecConfig)
	if err != nil {
		if !firstConnection {
			// The device may still be returning after sleep or a driver restart.
			return fmt.Errorf("%w: create audio player: %w", voiceclient.ErrConnectionLost, err)
		}
		return fmt.Errorf("create audio player: %w", err)
	}
	playerClosed := false
	defer func() {
		if !playerClosed {
			if err := player.Close(); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("close audio player: %w", err))
			}
		}
	}()
	detachPlayer, err := state.Audio.AttachPlayer(player)
	if err != nil {
		return err
	}
	defer detachPlayer()
	supervisor := newLoopSupervisor(parent)
	ctx := supervisor.Context()
	stopNotification := context.AfterFunc(ctx, stopSession)
	defer stopNotification()
	defer supervisor.Cancel()
	networkLoop := func(operation string, loop func() error) error {
		return classifyNetworkLoopError(operation, loop())
	}
	supervisor.GoNamed("decode", func(ctx context.Context) error {
		return voiceclient.DecodeLoop(ctx, func() (audio.Decoder, error) { return audio.NewOpusDecoder(codecConfig) }, orderedInCh, decodedCh, state)
	})
	supervisor.GoNamed("jitter", func(ctx context.Context) error {
		return voiceclient.JitterLoop(ctx, encodedInCh, orderedInCh, voiceclient.DefaultJitterDepth, state)
	})
	supervisor.GoNamed("heartbeat", func(ctx context.Context) error {
		return networkLoop("heartbeat", func() error { return voiceclient.HeartbeatLoop(ctx, conn, state) })
	})
	supervisor.GoNamed("receive", func(ctx context.Context) error {
		return networkLoop("receive", func() error { return voiceclient.ReceiveLoop(ctx, conn, encodedInCh, controlCh, state) })
	})
	supervisor.GoNamed("mix", func(ctx context.Context) error {
		return voiceclient.MixLoopWithState(ctx, decodedCh, pcmOutCh, 20*time.Millisecond, state)
	})
	supervisor.GoNamed("playback", func(ctx context.Context) error {
		return voiceclient.PlaybackLoopWithState(ctx, player, pcmOutCh, state)
	})
	supervisor.GoNamed("control", func(ctx context.Context) error { return voiceclient.ControlLoop(ctx, state, controlCh) })
	supervisor.GoNamed("speaking", func(ctx context.Context) error { return voiceclient.SpeakingLoop(ctx, state) })
	supervisor.GoNamed("notices", func(ctx context.Context) error { return voiceclient.ConsoleStateLoop(ctx, state, noticeOutput) })

	finish := func(cause error) error {
		shutdownErr := supervisor.Shutdown(func() error {
			closeErr := player.Close()
			playerClosed = true
			return wrapError("close audio player", closeErr)
		})
		return errors.Join(cause, shutdownErr)
	}
	logger.Printf("client connected: id=%d name=%s", sessionID, name)
	syncer := &voiceclient.StateSyncer{Conn: conn, State: state}
	snapshot, err := syncer.Load(ctx)
	if err != nil {
		return finish(fmt.Errorf("%w: load server snapshot: %v", voiceclient.ErrConnectionLost, err))
	}
	supervisor.GoNamed("state_sync", syncer.Run)
	channelID, err := defaultChannelForConnection(snapshot)
	if err != nil {
		return finish(err)
	}
	join := func(id domain.ChannelID) error { return voiceclient.JoinChannel(ctx, conn, state, id) }
	if !rejoinPreviousChannel(snapshot, previousChannel, channelID, join, logger) {
		if err := voiceclient.JoinChannel(ctx, conn, state, channelID); err != nil {
			return finish(fmt.Errorf("cannot join default channel %d: %w", channelID, err))
		}
	}
	if _, err := syncer.Load(ctx); err != nil {
		return finish(fmt.Errorf("%w: refresh server snapshot: %v", voiceclient.ErrConnectionLost, err))
	}
	currentSnapshot := state.Snapshot()
	activeProfile, err := channelAudioProfile(currentSnapshot, state.ChannelID())
	if err != nil {
		return finish(err)
	}
	if err := encoder.Configure(activeProfile); err != nil {
		return finish(fmt.Errorf("configure Opus encoder for channel %d: %w", state.ChannelID(), err))
	}
	supervisor.GoNamed("voice_telemetry", func(ctx context.Context) error {
		return networkLoop("voice telemetry", func() error { return voiceclient.VoiceTelemetryLoop(ctx, conn, state) })
	})
	supervisor.GoNamed("voice_state", func(ctx context.Context) error {
		return voiceclient.VoiceStateLoop(ctx, conn, state, encoder.Configure)
	})
	topologyUnchanged := voiceclient.SameChannelTopology(oldSnapshot, currentSnapshot)
	if !firstConnection && !topologyUnchanged {
		logger.Printf("reconnected; channel structure changed")
	} else if !firstConnection {
		logger.Printf("reconnected; channel restored: id=%d", state.ChannelID())
	}
	state.SetConnectionStatus(voiceclient.ConnectionConnected)
	recorder, err := streams.OpenRecorder(codecConfig)
	if err != nil {
		logger.Printf("capture unavailable: %v", err)
		if recorder == nil {
			return finish(fmt.Errorf("create audio recorder: %w", err))
		}
		state.Audio.SetCaptureAvailable(false)
	}
	deviceAvailability, hasAvailability := recorder.(captureAvailability)
	available := err == nil
	if hasAvailability {
		deviceAvailability.SetAvailabilityHandler(func(available bool) {
			if !available {
				logger.Printf("capture unavailable")
			}
			state.Audio.SetCaptureAvailable(available)
		})
		available = deviceAvailability.Available()
	}
	// AudioControlState survives reconnects. Restore the status on each open.
	state.Audio.SetCaptureAvailable(available)
	supervisor.GoNamed("encode", func(ctx context.Context) error {
		return voiceclient.EncodeLoopWithPipeline(ctx, encoder, filter, detector, gate, pcmCh, audioCh, state)
	})
	supervisor.GoNamed("capture", func(ctx context.Context) error {
		return voiceclient.RecordLoop(ctx, recorder, pcmCh, codecConfig.SamplesPerFrame, state.Audio)
	})
	supervisor.GoNamed("voice_send", func(ctx context.Context) error {
		return networkLoop("voice send", func() error { return voiceclient.SendLoopWithStats(ctx, conn, sessionID, audioCh, state.Audio, state) })
	})
	supervisor.GoNamed("audio_state", func(ctx context.Context) error {
		return networkLoop("audio state", func() error { return voiceclient.AudioStateLoop(ctx, conn, state) })
	})
	if managed, ok := streams.(interface{ RunDeviceChanges(context.Context) error }); ok {
		supervisor.GoNamed("audio_devices", managed.RunDeviceChanges)
	}
	if onReady != nil {
		onReady()
	}
	runtimeErr := supervisor.Wait(func() error {
		recorderErr := recorder.Close()
		playerErr := player.Close()
		playerClosed = true
		return errors.Join(wrapError("close audio recorder", recorderErr), wrapError("close audio player", playerErr))
	})
	if !errors.Is(runtimeErr, voiceclient.ErrConnectionLost) {
		runtimeErr = errors.Join(runtimeErr, wrapError("send disconnect", voiceclient.Disconnect(conn, sessionID)))
	}
	return runtimeErr
}

// rejoinPreviousChannel returns a reconnecting client to the channel it was in
// before the connection dropped. It reports false when there is nothing to
// restore or the server refused the join (the channel was deleted, filled up or
// locked meanwhile); the caller then falls back to the default channel.
func rejoinPreviousChannel(snapshot domain.ServerSnapshot, previous, defaultChannel domain.ChannelID, join func(domain.ChannelID) error, logger *log.Logger) bool {
	if previous == 0 || previous == defaultChannel {
		return false
	}
	known := false
	for _, channel := range snapshot.Channels {
		if channel.ID == previous {
			known = true
			break
		}
	}
	if !known {
		logger.Printf("reconnect: previous channel %d no longer exists; joining the default", previous)
		return false
	}
	if err := join(previous); err != nil {
		logger.Printf("reconnect: cannot rejoin channel %d: %v; joining the default", previous, err)
		return false
	}
	return true
}

func defaultChannelForConnection(snapshot domain.ServerSnapshot) (domain.ChannelID, error) {
	for _, channel := range snapshot.Channels {
		if channel.ID == snapshot.Info.DefaultChannelID && channel.MinJoinLevel == 0 && channel.MaxUsers == 0 {
			return channel.ID, nil
		}
	}
	return 0, errors.New("server has no unrestricted default channel")
}

func newMicrophoneFilter(config audio.CodecConfig, settings audiornnoise.Settings) (audio.PCMFilter, error) {
	if config.SampleRate != audiornnoise.SampleRate || config.Channels != 1 {
		return nil, fmt.Errorf("RNNoise requires 48000 Hz mono audio, got %d Hz with %d channels", config.SampleRate, config.Channels)
	}
	if config.SamplesPerFrame%audiornnoise.FrameSize != 0 {
		return nil, fmt.Errorf("RNNoise frame size %d does not divide client frame size %d", audiornnoise.FrameSize, config.SamplesPerFrame)
	}
	processor, err := audiornnoise.New(settings)
	if err != nil {
		return nil, fmt.Errorf("create RNNoise processor: %w", err)
	}
	return processor, nil
}

func newMicrophoneVAD(config audio.CodecConfig) (audiovad.Detector, error) {
	detector, err := audiovad.NewWebRTC(audiovad.WebRTCConfig{
		SampleRate:      config.SampleRate,
		Channels:        config.Channels,
		SamplesPerFrame: config.SamplesPerFrame,
		Aggressiveness:  audiovad.ModeAggressive,
	})
	if err != nil {
		return nil, fmt.Errorf("create WebRTC VAD: %w", err)
	}
	return detector, nil
}

type lockedOutput struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *lockedOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}

func classifyNetworkLoopError(operation string, err error) error {
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	if errors.Is(err, voiceclient.ErrConnectionLost) {
		return err
	}
	return fmt.Errorf("%w: %s: %w", voiceclient.ErrConnectionLost, operation, err)
}

func channelAudioProfile(snapshot domain.ServerSnapshot, channelID domain.ChannelID) (domain.AudioProfile, error) {
	for _, channel := range snapshot.Channels {
		if channel.ID == channelID {
			if err := domain.ValidateAudioProfile(channel.Audio); err != nil {
				return domain.AudioProfile{}, fmt.Errorf("channel %d audio profile: %w", channelID, err)
			}
			return channel.Audio, nil
		}
	}
	return domain.AudioProfile{}, fmt.Errorf("channel %d is missing from server snapshot", channelID)
}

func wrapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}
