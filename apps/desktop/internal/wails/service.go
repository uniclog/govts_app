package wailsui

import (
	"context"
	"log"
	"sync"
	"time"

	"uniclog.io/sonoryx/apps/desktop/internal/filequeue"
	"uniclog.io/sonoryx/apps/desktop/internal/hotkeys"
	"uniclog.io/sonoryx/apps/desktop/internal/nativeaudio"
	"uniclog.io/sonoryx/apps/desktop/internal/serverstatus"
	"uniclog.io/sonoryx/apps/desktop/internal/settings"
	clientapp "uniclog.io/sonoryx/internal/clientcore"
	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
)

const operationTimeout = 10 * time.Second

type validationError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e *validationError) Error() string { return e.Field + ": " + e.Message }

type Service struct {
	audio  *nativeaudio.Controller
	files  *filequeue.FileQueue
	client *clientapp.App
	preferences
	connectionState
	// audioSwitchMu orders microphone and speaker clicks with their cues.
	audioSwitchMu     sync.Mutex
	hotkeyKeys        map[string]string
	hotkeyMu          sync.Mutex
	hotkeyApplyMu     sync.Mutex
	hotkeyLifecycleMu sync.Mutex
	hotkeys           *hotkeys.Manager
	hotkeyErrors      map[string]error
	// Hotkey preferences and capture state are guarded by settingsMu.
	hotkeysStopped bool
	// hotkeyNotifier, hotkeyNotices and popupLook are guarded by hotkeyMu.
	hotkeyNotifications   bool
	hotkeysEnabled        bool
	hotkeyCapture         uint32
	hotkeyCaptureSeq      uint32
	hotkeyCaptureDeadline time.Time
	hotkeyCaptureTimer    *time.Timer
	hotkeyWindowInactive  bool
	hotkeyNotifier        *hotkeyNotifier
	hotkeyNotices         map[string]string
	popupLook             map[string]string
	messagePopup          func(title, body string) error // guarded by hotkeyMu
}

func NewService(client *clientapp.App) *Service {
	return &Service{client: client, connectionState: connectionState{serverStatus: serverstatus.NewCache()}, hotkeyKeys: clientsettings.NormalizeHotkeys(nil), hotkeyNotifications: true}
}

// LogDiagnostic receives infrequent browser-side lifecycle events. It accepts
// bounded text only; callers must not pass SDP, credentials or media content.
func (s *Service) LogDiagnostic(operation, message string) {
	if len(operation) > 128 {
		operation = operation[:128]
	}
	if len(message) > 2048 {
		message = message[:2048]
	}
	log.Printf("frontend diagnostic: operation=%q message=%q", operation, message)
}

func (s *Service) ParticipantVolume(sessionID string) (float32, error) {
	id, err := parseUint64(sessionID, "sessionId", false)
	if err != nil {
		return 0, err
	}
	return s.client.ParticipantVolume(id), nil
}

func (s *Service) SetParticipantVolume(sessionID string, value float32) error {
	id, err := parseUint64(sessionID, "sessionId", false)
	if err != nil {
		return err
	}
	if err := s.client.SetParticipantVolume(id, value); err != nil {
		return err
	}
	s.saveParticipantVolume(id, value)
	return nil
}

func (s *Service) JoinChannel(channelID string) error {
	id, err := parseUint64(channelID, "channelId", false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.JoinChannel(ctx, domain.ChannelID(id))
}

func (s *Service) Kick(sessionID string) error {
	id, err := parseUint64(sessionID, "sessionId", false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.Moderate(ctx, protocol.PacketKick, id, 0)
}

func (s *Service) Ban(sessionID string) error {
	id, err := parseUint64(sessionID, "sessionId", false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.Moderate(ctx, protocol.PacketBan, id, 0)
}

func (s *Service) Drag(sessionID, channelID string) error {
	id, err := parseUint64(sessionID, "sessionId", false)
	if err != nil {
		return err
	}
	channel, err := parseUint64(channelID, "channelId", false)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), operationTimeout)
	defer cancel()
	return s.client.Moderate(ctx, protocol.PacketDrag, id, domain.ChannelID(channel))
}

func (s *Service) Snapshot() ClientViewDTO {
	s.connectionMu.Lock()
	defer s.connectionMu.Unlock()
	dto := viewDTO(s.client.Snapshot(), s.client.LastError())
	dto.ServerIdentityChange = s.client.ServerIdentityChange()
	dto.ChatContext = s.client.ChatContext() + "|" + dto.UserID
	s.recordServerVisit(dto)
	return dto
}

func (s *Service) ConnectionStats() ConnectionStatsDTO {
	stats := s.client.ConnectionStats()
	return ConnectionStatsDTO{
		Voice: VoiceStatsDTO{ReceivedCopies: stats.Voice.ReceivedCopies, SizeDrops: stats.Voice.Drops.Size, ProtocolDrops: stats.Voice.Drops.Protocol,
			Incoming:    VoiceDirectionDTO{Known: stats.Voice.RemoteKnown, Desired: stats.Voice.Remote.DesiredFrames, Allowed: stats.Voice.Remote.AllowedFrames, Actual: stats.Voice.RemoteActual, Reason: stats.Voice.Remote.Reason, Limited: stats.Voice.RemoteLimited, Restored: stats.Voice.Incoming.Restored, Late: stats.Voice.Incoming.Late, PLC: stats.Voice.Incoming.PLC},
			Outgoing:    VoiceDirectionDTO{Known: connectionStatus(stats.Status) == "connected", Desired: stats.Voice.Local.DesiredFrames, Allowed: stats.Voice.Local.AllowedFrames, Actual: stats.Voice.Actual, Reason: stats.Voice.Local.Reason, Limited: stats.Voice.Limited, Restored: stats.Voice.Outgoing.Restored, Late: stats.Voice.Outgoing.Late},
			BudgetDrops: stats.Voice.Drops.Budget, RepairBudgetDrops: stats.Voice.Drops.RepairBudget, QueueDrops: stats.Voice.Drops.Queue, WriteFailures: stats.Voice.Drops.Write,
		},
		SessionID:              formatUint64(stats.SessionID),
		Generation:             formatUint64(stats.Generation),
		Status:                 connectionStatus(stats.Status),
		PingMS:                 stats.PingMS,
		PingVariationMS:        stats.PingVariationMS,
		PingAvailable:          stats.PingAvailable,
		PingVariationAvailable: stats.PingVariationAvailable,
		PingSampleAtMS:         stats.PingSampleAtMS,
		IncomingLoss:           stats.IncomingLoss,
		IncomingKnown:          stats.IncomingKnown,
		IncomingSampleAtMS:     stats.IncomingSampleAtMS,
		OutgoingLoss:           stats.OutgoingLoss,
		OutgoingKnown:          stats.OutgoingKnown,
		RecoveredLoss:          stats.RecoveredLoss,
		ConcealedLoss:          stats.ConcealedLoss,
		LossBurstsSingle:       stats.LossBurstsSingle,
		LossBurstsDouble:       stats.LossBurstsDouble,
		LossBurstsLong:         stats.LossBurstsLong,
	}
}

func (s *Service) EventsAfter(sequence string) ([]ClientEventDTO, error) {
	after, err := parseUint64(sequence, "sequence", true)
	if err != nil {
		return nil, err
	}
	events := s.client.EventsAfter(after)
	result := make([]ClientEventDTO, 0, len(events))
	for _, event := range events {
		result = append(result, eventDTO(event))
	}
	return result, nil
}
