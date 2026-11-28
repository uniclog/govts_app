package clientcore

import client "uniclog.io/sonoryx/internal/clientcore/internal/live"

type ClientViewState = client.ClientViewState
type ServerRequestError = client.ServerRequestError
type ConnectionStats = client.ConnectionStats
type ConnectionStatus = client.ConnectionStatus
type AudioMeterSample = client.AudioMeterSample
type NotificationSoundKind = client.NotificationSoundKind

const (
	ConnectionConnected        = client.ConnectionConnected
	ConnectionConnecting       = client.ConnectionConnecting
	ConnectionDisconnected     = client.ConnectionDisconnected
	ConnectionReconnecting     = client.ConnectionReconnecting
	DefaultParticipantVolume   = client.DefaultParticipantVolume
	NotificationMicMuted       = client.NotificationMicMuted
	NotificationMicUnmuted     = client.NotificationMicUnmuted
	NotificationSpeakerMuted   = client.NotificationSpeakerMuted
	NotificationSpeakerUnmuted = client.NotificationSpeakerUnmuted
)
