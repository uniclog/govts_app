// Package voice exposes the server fixture surface used by client integration tests.
package voice

import "uniclog.io/sonoryx/apps/server/internal/voice"

type Hub = voice.Hub
type Session = voice.Session

const (
	DefaultChannelID   = voice.DefaultChannelID
	DefaultChannelName = voice.DefaultChannelName
)

var (
	ErrAccountSessionLimit = voice.ErrAccountSessionLimit
	ErrDisplayNameTaken    = voice.ErrDisplayNameTaken
	ErrServerFull          = voice.ErrServerFull

	DispatchEvents          = voice.DispatchEvents
	HandleAudioStatePacket  = voice.HandleAudioStatePacket
	HandleVoicePacket       = voice.HandleVoicePacket
	HandleVoiceStateControl = voice.HandleVoiceStateControl
	HandleVoiceTelemetry    = voice.HandleVoiceTelemetry
	NewAuthenticator        = voice.NewAuthenticator
	NewHub                  = voice.NewHub
	NewRequestCache         = voice.NewRequestCache
	ServeUDP                = voice.ServeUDP
)
