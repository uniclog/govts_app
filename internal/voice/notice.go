package voice

import (
	"log"
	"net"
	"sync"
	"time"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

// arrivalInterval bounds arrival notices per account, so a client that loses
// its connection and reconnects in a loop cannot flood everybody's desktop.
// A deliberate exit (PacketDisconnect) lets the next entry through sooner:
// arrivalAfterExit after the first exit, five times longer after each further
// exit within exitStreakWindow (3 s, 15 s, then only the minute rule). Leaving
// and coming back is a real arrival; a connect/leave loop quickly falls back
// to one a minute.
const (
	arrivalInterval  = time.Minute
	arrivalAfterExit = 3 * time.Second
	exitStreakWindow = 2 * time.Minute
)

// arrivalExit is the exit streak of an account. allowed is when its next
// entry may be announced despite the minute rule; zero once used or when the
// streak is too long to shorten anything.
type arrivalExit struct {
	count   int
	at      time.Time
	allowed time.Time
}

// ForgetArrival runs after an explicit exit. Once the account has no session
// left on the server, its next arrival waits only the exit gap.
func (h *Hub) ForgetArrival(userID int64, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, session := range h.sessions {
		if session.UserID == userID {
			return
		}
	}
	streak := h.exits[userID]
	if now.Sub(streak.at) < exitStreakWindow {
		streak.count++
	} else {
		streak.count = 1
	}
	streak.at = now
	gap := arrivalAfterExit
	for i := 1; i < streak.count && gap < arrivalInterval; i++ {
		gap *= 5
	}
	streak.allowed = time.Time{}
	if gap < arrivalInterval {
		streak.allowed = now.Add(gap)
	}
	h.exits[userID] = streak
}

// SetNoticePrefs enables notices for a session and records its subscription.
// It reports whether the arrival of the session's account should be announced
// now: requested, not announced for this session and not announced recently.
func (h *Hub) SetNoticePrefs(id uint64, flags byte, now time.Time) (Session, bool, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	session, ok := h.sessions[id]
	if !ok {
		return Session{}, false, ErrSessionNotFound
	}
	session.notices = true
	session.subscribed = flags&protocol.NoticeSubscribed != 0
	announce := false
	if flags&protocol.NoticeAnnounce != 0 && !session.announced {
		session.announced = true
		last, seen := h.arrivals[session.UserID]
		exit, left := h.exits[session.UserID]
		afterExit := left && !exit.allowed.IsZero() && !now.Before(exit.allowed)
		if !seen || now.Sub(last) >= arrivalInterval || afterExit {
			announce = true
			h.arrivals[session.UserID] = now
			// One exit lets one entry through; a later reconnect after a
			// lost connection waits the minute again.
			if left {
				exit.allowed = time.Time{}
				h.exits[session.UserID] = exit
			}
		}
		for user, at := range h.arrivals {
			if now.Sub(at) >= arrivalInterval {
				delete(h.arrivals, user)
			}
		}
		for user, streak := range h.exits {
			if now.Sub(streak.at) >= exitStreakWindow {
				delete(h.exits, user)
			}
		}
	}
	return *cloneSession(session), announce, nil
}

func HandleNoticePrefsPacket(conn *udp.ServerPacketConn, hub *Hub, cache *RequestCache, packet protocol.VoicePacket, addr *net.UDPAddr) error {
	if err := ValidateSessionAddr(hub, packet.SessionID, addr); err != nil {
		return err
	}
	if packet.RequestID == 0 {
		return SendError(conn, addr, packet.SessionID, 0, "notice request ID is required")
	}
	if response, ok := cache.Get(packet.SessionID, packet.RequestID); ok {
		return conn.WritePacket(packet.SessionID, addr, response)
	}
	flags, err := protocol.DecodeNoticePrefs(packet.Payload)
	if err != nil {
		return cacheAndSendSessionError(conn, cache, packet, addr, err.Error())
	}
	session, announce, err := hub.SetNoticePrefs(packet.SessionID, flags, time.Now())
	if err != nil {
		return cacheAndSendSessionError(conn, cache, packet, addr, err.Error())
	}
	ack := protocol.VoicePacket{Type: protocol.PacketNoticePrefsAck, SessionID: packet.SessionID, RequestID: packet.RequestID, Payload: protocol.EncodeNoticePrefs(flags)}
	cache.Put(packet.SessionID, packet.RequestID, ack)
	if err := SendToSession(conn, session, ack); err != nil {
		return err
	}
	if announce {
		queueNotice(conn, hub.arrivalRecipients(session.UserID), protocol.Notice{Kind: protocol.NoticeArrival, UserID: session.UserID, Name: session.Name, SessionID: session.ID})
	}
	return nil
}

// Arrival notices go to everybody, so their fan-out runs off the UDP loop:
// encrypting a packet per recipient there would hold up voice forwarding.
// The queue is small and drops when full; a notice is only a hint.
type noticeJob struct {
	conn       *udp.ServerPacketConn
	recipients []noticeTarget
	notice     protocol.Notice
}

var (
	noticeQueue      = make(chan noticeJob, 64)
	noticeSenderOnce sync.Once
)

func queueNotice(conn *udp.ServerPacketConn, recipients []noticeTarget, notice protocol.Notice) {
	if len(recipients) == 0 {
		return
	}
	noticeSenderOnce.Do(func() {
		go func() {
			for job := range noticeQueue {
				sendNotice(job.conn, job.recipients, job.notice)
			}
		}()
	})
	select {
	case noticeQueue <- noticeJob{conn: conn, recipients: recipients, notice: notice}:
	default:
		log.Printf("notice dropped: queue full kind=%d user_id=%d recipients=%d", notice.Kind, notice.UserID, len(recipients))
	}
}

// sendMessageNotice tells everyone who may read a new message about it. The
// notice carries an excerpt, so a watcher needs no chat access to show it.
func sendMessageNotice(conn *udp.ServerPacketConn, hub *Hub, target domain.ChatTarget, message domain.ChatMessage) {
	recipients, channelName := hub.messageRecipients(message.SenderID, target)
	if len(recipients) == 0 {
		return
	}
	notice := protocol.Notice{Kind: protocol.NoticeMessage, UserID: message.SenderID, Name: message.SenderName, MessageID: message.ID, Attachments: len(message.AttachmentIDs) > 0}
	notice.Text, _ = protocol.NoticeExcerpt(message.Text)
	if target.Kind == domain.ChatChannel {
		notice.Target, notice.ChannelName = target, channelName
	} else {
		// The recipient sees the dialog under the sender's id.
		notice.Target = domain.ChatTarget{Kind: domain.ChatDirect, ID: message.SenderID}
	}
	sendNotice(conn, recipients, notice)
}

func sendNotice(conn *udp.ServerPacketConn, recipients []noticeTarget, notice protocol.Notice) {
	payload, err := protocol.EncodeNotice(notice)
	if err != nil {
		log.Printf("notice skipped: kind=%d user_id=%d error=%v", notice.Kind, notice.UserID, err)
		return
	}
	// Notices are hints: a lost one is simply not shown, like ChatChanged.
	for _, recipient := range recipients {
		_ = conn.WritePacket(recipient.ID, recipient.Addr, protocol.VoicePacket{Type: protocol.PacketNotice, SessionID: recipient.ID, Payload: payload})
	}
}
