package voice

import (
	"context"
	"errors"
	"log"
	"net"
	"sync"
	"time"
	"unicode/utf8"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

type chatJob struct {
	packet  protocol.VoicePacket
	addr    *net.UDPAddr
	request protocol.ChatRequest
	user    int64
	// unchanged marks a repeated reaction: there is nothing to announce.
	unchanged bool
}

type chatRate struct {
	at                         time.Time
	requests, sends, reactions int
}

type chatWorker struct {
	auth    *Authenticator
	conn    *udp.ServerPacketConn
	hub     *Hub
	queue   chan chatJob
	mu      sync.Mutex
	pending map[int64]int
	rate    map[int64]chatRate
}

func newChatWorker(a *Authenticator, conn *udp.ServerPacketConn, hub *Hub) *chatWorker {
	return &chatWorker{auth: a, conn: conn, hub: hub, queue: make(chan chatJob, 128), pending: make(map[int64]int), rate: make(map[int64]chatRate)}
}

func (w *chatWorker) enqueue(packet protocol.VoicePacket, addr *net.UDPAddr) error {
	if err := ValidateSessionAddr(w.hub, packet.SessionID, addr); err != nil {
		return err
	}
	r, err := protocol.DecodeChatRequest(packet.Payload)
	if err != nil || packet.RequestID == 0 {
		return SendError(w.conn, addr, packet.SessionID, packet.RequestID, "некорректный запрос чата")
	}
	s, ok := w.hub.Get(packet.SessionID)
	if !ok || s.UserID <= 0 {
		return SendError(w.conn, addr, packet.SessionID, packet.RequestID, "требуется авторизация")
	}
	w.mu.Lock()
	now := time.Now()
	if len(w.rate) >= 8192 {
		for id, rate := range w.rate {
			if now.Sub(rate.at) >= time.Minute {
				delete(w.rate, id)
			}
		}
	}
	rate, known := w.rate[s.UserID]
	if now.Sub(rate.at) >= time.Minute {
		rate = chatRate{at: now}
	}
	// A momentary backlog is not abuse: clients pause for a minute on the limit text below.
	if w.pending[s.UserID] >= 4 {
		w.mu.Unlock()
		return SendError(w.conn, addr, packet.SessionID, packet.RequestID, "чат занят; повторите позже")
	}
	// Messages and reactions have their own budgets, so polling cannot block them.
	// Clients pause background reads for a minute on the read limit text only.
	limit := ""
	switch {
	case !known && len(w.rate) >= 8192:
		limit = "лимит запросов чата; повторите позже"
	case r.Operation == protocol.ChatSend:
		if rate.sends >= 30 {
			limit = "лимит сообщений чата; повторите позже"
		}
		rate.sends++
	case r.Operation == protocol.ChatReact:
		if rate.reactions >= 120 {
			limit = "лимит сообщений чата; повторите позже"
		}
		rate.reactions++
	default:
		if rate.requests >= 240 {
			limit = "лимит запросов чата; повторите позже"
		}
		rate.requests++
	}
	if limit != "" {
		w.mu.Unlock()
		return SendError(w.conn, addr, packet.SessionID, packet.RequestID, limit)
	}
	w.rate[s.UserID] = rate
	w.pending[s.UserID]++
	select {
	case w.queue <- chatJob{packet: packet, addr: cloneUDPAddr(addr), request: r, user: s.UserID}:
		w.mu.Unlock()
		return nil
	default:
		w.pending[s.UserID]--
		if w.pending[s.UserID] == 0 {
			delete(w.pending, s.UserID)
		}
		w.mu.Unlock()
		return SendError(w.conn, addr, packet.SessionID, packet.RequestID, "чат занят; повторите позже")
	}
}

func (w *chatWorker) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-w.queue:
			w.handle(ctx, job)
			w.mu.Lock()
			w.pending[job.user]--
			if w.pending[job.user] == 0 {
				delete(w.pending, job.user)
			}
			w.mu.Unlock()
		}
	}
}

func (w *chatWorker) check(job chatJob) (Session, error) {
	if err := ValidateSessionAddr(w.hub, job.packet.SessionID, job.addr); err != nil {
		return Session{}, domain.ChatRejection("сессия изменилась")
	}
	s, ok := w.hub.Get(job.packet.SessionID)
	if !ok || s.UserID != job.user {
		return s, domain.ChatRejection("сессия изменилась")
	}
	if job.request.Operation == protocol.ChatDialogs {
		return s, nil
	}
	t := job.request.Target
	if t.Kind == domain.ChatChannel {
		if s.ChannelID != domain.ChannelID(t.ID) {
			return s, domain.ChatRejection("чат доступен только участникам текущего канала")
		}
		for _, c := range w.hub.ListChannels() {
			if c.ID == s.ChannelID && s.JoinLevel >= c.MinJoinLevel {
				return s, nil
			}
		}
		return s, ErrPermissionDenied
	}
	if t.ID == s.UserID {
		return s, domain.ChatRejection("нельзя открыть диалог с самим собой")
	}
	return s, nil
}

func (w *chatWorker) lockPolicy() {
	if w.auth.policyGate != nil {
		w.auth.policyGate.Lock()
	}
}
func (w *chatWorker) unlockPolicy() {
	if w.auth.policyGate != nil {
		w.auth.policyGate.Unlock()
	}
}

func (w *chatWorker) handle(parent context.Context, job chatJob) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	w.lockPolicy()
	actor, err := w.check(job)
	w.unlockPolicy()
	var page domain.ChatPage
	if err == nil {
		account, e := w.auth.store.Account(ctx, actor.UserID)
		if e != nil || account.Banned {
			err = domain.ChatRejection("доступ к чату запрещён")
		}
	}
	if err == nil && job.request.Operation != protocol.ChatDialogs && job.request.Target.Kind == domain.ChatDirect {
		if _, e := w.auth.store.Account(ctx, job.request.Target.ID); e != nil {
			err = domain.ErrChatRecipientUnavailable
		}
	}
	if err == nil {
		r := job.request
		switch r.Operation {
		case protocol.ChatSend:
			var m domain.ChatMessage
			m, err = w.auth.store.SaveChatFiles(ctx, actor.UserID, actor.Name, r.Target, r.ClientID, r.Text, r.AttachmentIDs)
			page = domain.ChatPage{UserID: actor.UserID, LatestID: m.ID, Cursor: m.ID, Messages: []domain.ChatMessage{m}}
		case protocol.ChatHistory:
			page, err = w.auth.store.ChatHistory(ctx, actor.UserID, r.Target, r.Cursor, r.Forward)
		case protocol.ChatChanges:
			if r.Forward {
				page, err = w.auth.store.ChatChangeWatermark(ctx, actor.UserID, r.Target)
			} else {
				page, err = w.auth.store.ChatChanges(ctx, actor.UserID, r.Target, r.Cursor)
			}
		case protocol.ChatDelete, protocol.ChatDeleteFile:
			var fileID int64
			if r.Operation == protocol.ChatDeleteFile {
				fileID = r.AttachmentIDs[0]
			}
			var message domain.ChatMessage
			message, err = w.auth.store.DeleteChat(ctx, actor.UserID, r.Target, r.Cursor, fileID)
			page = domain.ChatPage{UserID: actor.UserID, Messages: []domain.ChatMessage{message}}
		case protocol.ChatReact:
			code, _ := domain.ChatReactionCode(r.Text)
			var message domain.ChatMessage
			var changed bool
			message, changed, err = w.auth.store.ReactChat(ctx, actor.UserID, r.Target, r.Cursor, code, r.Forward)
			job.unchanged = !changed
			page = domain.ChatPage{UserID: actor.UserID, Messages: []domain.ChatMessage{message}}
		case protocol.ChatDialogs:
			page, err = w.auth.store.ChatDialogs(ctx, actor.UserID, r.Cursor)
		case protocol.ChatRead:
			err = w.auth.store.ReadChat(ctx, actor.UserID, r.Target, r.Cursor)
			page = domain.ChatPage{UserID: actor.UserID}
		}
	}
	w.complete(ctx, job, actor, page, err)
}

// chatErrorText returns the reply for a failed chat request. Rejections and
// permission errors are shown as is; storage and session errors are logged and
// replaced with a generic text.
func chatErrorText(job chatJob, err error) string {
	var rejection domain.ChatRejection
	if errors.As(err, &rejection) {
		return rejection.Error()
	}
	if errors.Is(err, ErrPermissionDenied) {
		return ErrPermissionDenied.Error()
	}
	log.Printf("chat request failed: session_id=%d user_id=%d operation=%d error=%v", job.packet.SessionID, job.user, job.request.Operation, err)
	return "хранилище чата недоступно; повторите позже"
}

// complete keeps post-commit delivery separate from authorization and storage.
func (w *chatWorker) complete(ctx context.Context, job chatJob, actor Session, page domain.ChatPage, err error) {
	var payload []byte
	if err == nil {
		payload, err = fitChatPage(&page, job.request)
	}
	w.lockPolicy()
	// Revalidate reads before exposing history. Writes were authorized on
	// acceptance and committed by the store: a later channel change cannot
	// turn a successful commit into a rejection or suppress its notifications.
	if job.request.Operation == protocol.ChatHistory || job.request.Operation == protocol.ChatDialogs || job.request.Operation == protocol.ChatChanges {
		if _, e := w.check(job); e != nil {
			err = e
		}
	}
	w.unlockPolicy()
	if err != nil {
		_ = SendError(w.conn, job.addr, job.packet.SessionID, job.packet.RequestID, chatErrorText(job, err))
		return
	}
	ack := protocol.VoicePacket{Type: protocol.PacketChatAck, SessionID: actor.ID, RequestID: job.packet.RequestID, Payload: payload}
	if err := w.conn.WritePacket(actor.ID, job.addr, ack); err != nil {
		log.Printf("chat ack failed: session_id=%d error=%v", actor.ID, err)
	}
	if job.request.Operation != protocol.ChatSend && job.request.Operation != protocol.ChatRead && job.request.Operation != protocol.ChatDelete && job.request.Operation != protocol.ChatDeleteFile && job.request.Operation != protocol.ChatReact {
		return
	}
	if job.unchanged {
		return
	}
	// Notifications are hints; periodic cursor reconciliation recovers packet loss.
	for _, recipient := range w.hub.Inspect().Sessions {
		eligible := recipient.UserID == actor.UserID
		if job.request.Operation != protocol.ChatRead {
			if job.request.Target.Kind == domain.ChatChannel {
				eligible = recipient.ChannelID == domain.ChannelID(job.request.Target.ID)
			} else {
				eligible = eligible || recipient.UserID == job.request.Target.ID
			}
		}
		// The reacting session already holds the new state in its ack.
		if eligible && (job.request.Operation != protocol.ChatReact || recipient.ID != actor.ID) {
			_ = w.conn.WritePacket(recipient.ID, recipient.Addr, protocol.VoicePacket{Type: protocol.PacketChatChanged, SessionID: recipient.ID})
		}
	}
}

func fitChatPage(p *domain.ChatPage, r protocol.ChatRequest) ([]byte, error) {
	// Preserve the nearest messages: ascending for catch-up, descending for older pages.
	for {
		if r.Operation == protocol.ChatChanges && len(p.Messages) > 0 {
			p.Cursor = p.Messages[len(p.Messages)-1].Revision
		}
		if r.Operation == protocol.ChatHistory && len(p.Messages) > 0 {
			p.Cursor = p.Messages[len(p.Messages)-1].ID
		}
		if r.Operation == protocol.ChatDialogs && len(p.Dialogs) > 0 {
			p.Cursor = p.Dialogs[len(p.Dialogs)-1].UserID
		}
		b, err := protocol.EncodeChatPage(*p)
		if err == nil {
			return b, nil
		}
		if len(p.Messages) > 1 {
			p.Messages = p.Messages[:len(p.Messages)-1]
			p.HasMore = true
			continue
		}
		if len(p.Dialogs) > 1 {
			p.Dialogs = p.Dialogs[:len(p.Dialogs)-1]
			p.HasMore = true
			continue
		}
		// A full message that also carries a revision and reactions can exceed one
		// datagram. Shortening the display name keeps it deliverable.
		if len(p.Messages) == 1 && p.Messages[0].SenderName != "" && errors.Is(err, protocol.ErrPayloadTooLarge) {
			name := p.Messages[0].SenderName
			_, size := utf8.DecodeLastRuneInString(name)
			p.Messages[0].SenderName = name[:len(name)-size]
			continue
		}
		return nil, err
	}
}
