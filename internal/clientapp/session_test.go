package clientapp

import (
	"testing"

	"uniclog.io/sonoryx/internal/domain"
)

func TestClientAudioConfigUsesDefaultAudioProfile(t *testing.T) {
	profile := domain.DefaultAudioProfile()
	config := clientAudioConfig()

	if config.SampleRate != int(profile.SampleRate) {
		t.Fatalf("sample rate = %d, want %d", config.SampleRate, profile.SampleRate)
	}
	if config.Channels != int(profile.Channels) {
		t.Fatalf("channels = %d, want %d", config.Channels, profile.Channels)
	}
	wantSamplesPerFrame := int(profile.SampleRate) * int(profile.FrameDurationMS) / 1000
	if config.SamplesPerFrame != wantSamplesPerFrame {
		t.Fatalf("samples per frame = %d, want %d", config.SamplesPerFrame, wantSamplesPerFrame)
	}
	if config.Bitrate != int(profile.Bitrate) {
		t.Fatalf("bitrate = %d, want %d", config.Bitrate, profile.Bitrate)
	}
	if config.Application != profile.Application {
		t.Fatalf("application = %d, want %d", config.Application, profile.Application)
	}
}

func TestChannelAudioProfileAllowsServerBitrateAndApplication(t *testing.T) {
	profile := domain.DefaultAudioProfile()
	profile.Bitrate = 48_000
	profile.Application = domain.OpusApplicationAudio
	snapshot := domain.ServerSnapshot{Channels: []domain.Channel{{ID: 7, Audio: profile}}}

	got, err := channelAudioProfile(snapshot, 7)
	if err != nil {
		t.Fatal(err)
	}
	if got != profile {
		t.Fatalf("profile = %+v, want %+v", got, profile)
	}
}
