package audio

import (
	"testing"

	"uniclog.io/sonoryx/internal/domain"
	pionopus "github.com/pion/opus"
)

func TestNewOpusEncoderAcceptsConfiguredBitrate(t *testing.T) {
	_, err := NewOpusEncoder(CodecConfig{
		SampleRate:      48_000,
		Channels:        1,
		SamplesPerFrame: 960,
		Bitrate:         48_000,
	})
	if err != nil {
		t.Fatalf("create encoder with configured bitrate: %v", err)
	}
}

func TestNewOpusEncoderRejectsInvalidConfiguredBitrate(t *testing.T) {
	_, err := NewOpusEncoder(CodecConfig{
		SampleRate:      48_000,
		Channels:        1,
		SamplesPerFrame: 960,
		Bitrate:         5_999,
	})
	if err == nil {
		t.Fatal("expected invalid bitrate error")
	}
}

func TestNewOpusEncoderAllowsDefaultBitrate(t *testing.T) {
	_, err := NewOpusEncoder(CodecConfig{
		SampleRate:      48_000,
		Channels:        1,
		SamplesPerFrame: 960,
	})
	if err != nil {
		t.Fatalf("create encoder with library default bitrate: %v", err)
	}
}

func TestOpusApplication(t *testing.T) {
	tests := []struct {
		name string
		in   domain.OpusApplication
		want pionopus.Application
	}{
		{name: "legacy default", want: pionopus.ApplicationAudio},
		{name: "audio", in: domain.OpusApplicationAudio, want: pionopus.ApplicationAudio},
		{name: "voip", in: domain.OpusApplicationVoIP, want: pionopus.ApplicationVoIP},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := opusApplication(test.in)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("application = %d, want %d", got, test.want)
			}
		})
	}
}

func TestOpusApplicationRejectsUnsupportedValue(t *testing.T) {
	if _, err := opusApplication(domain.OpusApplication(255)); err == nil {
		t.Fatal("expected unsupported application error")
	}
}

func TestOpusEncoderConfigureChannelProfile(t *testing.T) {
	encoder, err := NewOpusEncoder(CodecConfig{SampleRate: 48_000, Channels: 1, SamplesPerFrame: 960})
	if err != nil {
		t.Fatal(err)
	}
	profile := domain.DefaultAudioProfile()
	profile.Bitrate = 32_000
	profile.Application = domain.OpusApplicationVoIP
	if err := encoder.Configure(profile); err != nil {
		t.Fatalf("configure encoder: %v", err)
	}
}
