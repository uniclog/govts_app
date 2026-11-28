package audio

import "uniclog.io/sonoryx/internal/domain"

// Session opens PCM streams for one voice session.
type Session interface {
	OpenRecorder(CodecConfig) (Recorder, error)
	OpenPlayer(CodecConfig) (DeafenPlayer, error)
}

func DefaultCodecConfig() CodecConfig {
	profile := domain.DefaultAudioProfile()

	return CodecConfig{
		SampleRate:      int(profile.SampleRate),
		Channels:        int(profile.Channels),
		SamplesPerFrame: int(profile.SampleRate) * int(profile.FrameDurationMS) / 1000,
		Bitrate:         int(profile.Bitrate),
		Application:     profile.Application,
	}
}
