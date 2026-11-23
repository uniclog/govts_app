package clientapp

import (
	"context"
	"time"

	"uniclog.io/sonoryx/internal/audio"
	voiceclient "uniclog.io/sonoryx/internal/client"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

// noticeState is the notification side of the main connection. Guarded by
// App.noticeMu, which is never held while calling the sink or the network.
type noticeState struct {
	sink       NoticeSink
	online     bool
	subscribed func(server string) bool
	// announced: the arrival of the current Connect was requested, so a
	// reconnect after a network loss does not pop up for everybody again.
	announced bool
	// supported: the server acknowledged the preferences of this session.
	supported bool
	session   uint64
}

// SetNoticeSink installs the receiver of notices from every source.
func (a *App) SetNoticeSink(sink NoticeSink) {
	a.noticeMu.Lock()
	a.notice.sink = sink
	a.noticeMu.Unlock()
}

// SetNoticePresence and SetNoticeSubscription describe what the main session
// asks the server for. Call RefreshNotices after changing them.
func (a *App) SetNoticePresence(online bool) {
	a.noticeMu.Lock()
	a.notice.online = online
	a.noticeMu.Unlock()
}

func (a *App) SetNoticeSubscription(subscribed func(server string) bool) {
	a.noticeMu.Lock()
	a.notice.subscribed = subscribed
	a.noticeMu.Unlock()
}

// NoticesSupported reports whether the current session receives notices.
func (a *App) NoticesSupported() bool {
	a.noticeMu.Lock()
	defer a.noticeMu.Unlock()
	return a.notice.supported && a.notice.session == a.state.SessionID()
}

// CurrentServer is the endpoint of the active connection, or "".
func (a *App) CurrentServer() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.active {
		return ""
	}
	return a.serverEndpoint.String()
}

// ConnectionPassword is the password of the active connection. A subscription
// keeps it so the watcher can enter a protected server later.
func (a *App) ConnectionPassword() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.active {
		return ""
	}
	return a.connectionPassword
}

func (a *App) deliverNotice(server string, watcher bool, notice protocol.Notice) {
	a.noticeMu.Lock()
	sink := a.notice.sink
	a.noticeMu.Unlock()
	if sink != nil {
		sink(server, watcher, notice)
	}
}

// mainNotice receives PacketNotice of the main session.
func (a *App) mainNotice(notice protocol.Notice) {
	a.mu.Lock()
	server := a.serverEndpoint.String()
	a.mu.Unlock()
	a.deliverNotice(server, false, notice)
}

// RefreshNotices sends the current preferences to the connected server.
func (a *App) RefreshNotices() {
	a.mu.Lock()
	conn, chat := a.currentConn, a.chatSession
	a.mu.Unlock()
	if conn == nil || chat == nil || a.state.ConnectionStatus() != voiceclient.ConnectionConnected {
		return
	}
	go a.publishNotices(chat.ctx, conn)
}

func (a *App) publishNotices(ctx context.Context, conn *udp.ClientPacketConn) {
	a.noticePublish.Lock()
	defer a.noticePublish.Unlock()
	a.mu.Lock()
	current, server := a.currentConn, a.serverEndpoint.String()
	a.mu.Unlock()
	if current == nil || current != conn || ctx.Err() != nil {
		return
	}
	session := a.state.SessionID()
	a.noticeMu.Lock()
	var flags byte
	if a.notice.subscribed != nil && a.notice.subscribed(server) {
		flags |= protocol.NoticeSubscribed
	}
	announce := a.notice.online && !a.notice.announced
	if announce {
		flags |= protocol.NoticeAnnounce
	}
	a.noticeMu.Unlock()
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := voiceclient.SetNoticePrefs(requestCtx, current, a.state, flags)
	a.noticeMu.Lock()
	a.notice.supported, a.notice.session = err == nil, session
	if err == nil && announce {
		a.notice.announced = true
	}
	a.noticeMu.Unlock()
	if err != nil {
		a.logger.Printf("notice preferences not accepted: server=%s error=%v", server, err)
	}
}

// resetNotices starts a new Connect: its first session may announce again.
func (a *App) resetNotices() {
	a.noticeMu.Lock()
	a.notice.announced, a.notice.supported, a.notice.session = false, false, 0
	a.noticeMu.Unlock()
}

// PlayMessageCue plays the incoming message sound with or without a session.
// Without one it opens the playback device for the cue only; a cue that is
// already playing absorbs the next, so a burst of messages makes one sound.
func (a *App) PlayMessageCue() {
	if a.sessionPlaying() {
		a.state.PlayNotification(voiceclient.NotificationMessageReceived)
		return
	}
	if deafened, _ := a.state.Audio.PlaybackSnapshot(); deafened {
		return
	}
	pack, volume := a.state.Audio.SoundSnapshot()
	if volume <= 0 || !a.standaloneCue.TryLock() {
		return
	}
	go func() {
		defer a.standaloneCue.Unlock()
		player, err := audio.NewMalgoPlayer(clientAudioConfig(), a.AudioDeviceSelection().PlaybackID)
		if err != nil {
			a.logger.Printf("message cue unavailable: error=%v", err)
			return
		}
		defer func() { _ = player.Close() }()
		frame := clientAudioConfig()
		frameSamples := frame.SamplesPerFrame * frame.Channels
		if frameSamples <= 0 {
			frameSamples = 960
		}
		samples := voiceclient.RenderNotificationPCM(pack, volume, voiceclient.NotificationMessageReceived)
		_ = writePreviewPCM(a.lifetimeCtx, player, samples, frameSamples)
	}()
}
