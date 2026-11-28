package voice

import (
	"context"
	"errors"
	"log"
	"net"
	"time"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/protocol"
	"uniclog.io/sonoryx/internal/transport/udp"
)

// Each session may send this many new administration requests per window;
// retransmissions answered from the request cache are not counted.
const (
	maxAdminRequestsPerWindow = 30
	adminRequestWindow        = 10 * time.Second
)

// SetAdministration enables PacketAdminRequest. Configure it before starting
// ServeUDP.
func (a *Authenticator) SetAdministration(admin *Administration) { a.admin = admin }

func (a *Authenticator) allowAdminRequest(sessionID uint64, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.adminRate == nil {
		a.adminRate = make(map[uint64]authRateWindow)
	}
	for id, current := range a.adminRate {
		if now.Sub(current.started) >= adminRequestWindow {
			delete(a.adminRate, id)
		}
	}
	current := a.adminRate[sessionID]
	if current.started.IsZero() {
		current.started = now
	}
	if current.count >= maxAdminRequestsPerWindow {
		return false
	}
	current.count++
	a.adminRate[sessionID] = current
	return true
}

func (a *Authenticator) HandleAdmin(conn *udp.ServerPacketConn, hub *Hub, cache *RequestCache, packet protocol.VoicePacket, addr *net.UDPAddr) error {
	if err := ValidateSessionAddr(hub, packet.SessionID, addr); err != nil {
		return err
	}
	if packet.RequestID == 0 {
		return SendError(conn, addr, packet.SessionID, 0, "admin request ID is required")
	}
	if response, ok := cache.Get(packet.SessionID, packet.RequestID); ok {
		return conn.WritePacket(packet.SessionID, addr, response)
	}
	if a.admin == nil {
		return cacheAndSendSessionError(conn, cache, packet, addr, errAdminUnavailable.Error())
	}
	actorSession, ok := hub.Get(packet.SessionID)
	if !ok || actorSession.UserID <= 0 {
		return cacheAndSendSessionError(conn, cache, packet, addr, ErrPermissionDenied.Error())
	}
	actor := adminActorForSession(actorSession)
	if !actor.allowed() {
		return cacheAndSendSessionError(conn, cache, packet, addr, ErrPermissionDenied.Error())
	}
	if !a.allowAdminRequest(packet.SessionID, time.Now()) {
		return SendError(conn, addr, packet.SessionID, packet.RequestID, "too many admin requests; retry later")
	}
	request, err := protocol.DecodeAdminRequest(packet.Payload)
	if err != nil {
		return cacheAndSendSessionError(conn, cache, packet, addr, "invalid admin request")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response, removed, err := a.executeAdmin(ctx, hub, actor, request)
	for _, id := range removed {
		a.codec.Remove(id)
		cache.RemoveSession(id)
	}
	if request.Operation != protocol.AdminAccounts && request.Operation != protocol.AdminAudit && request.Operation != protocol.AdminServerPassword {
		logAdminChange(actor, request.Operation, request.TargetID, err)
	}
	if err != nil {
		return cacheAndSendSessionError(conn, cache, packet, addr, err.Error())
	}
	payload, err := protocol.EncodeAdminResponse(response)
	if err != nil {
		return cacheAndSendSessionError(conn, cache, packet, addr, "admin response unavailable")
	}
	ack := protocol.VoicePacket{Type: protocol.PacketAdminAck, SessionID: packet.SessionID, RequestID: packet.RequestID, Payload: payload}
	cache.Put(packet.SessionID, packet.RequestID, ack)
	return conn.WritePacket(packet.SessionID, addr, ack)
}

func (a *Authenticator) executeAdmin(ctx context.Context, hub *Hub, actor AdminActor, request protocol.AdminRequest) (protocol.AdminResponse, []uint64, error) {
	response := protocol.AdminResponse{Operation: request.Operation}
	target := int64(request.TargetID)
	var change AdminChange
	var err error
	switch request.Operation {
	case protocol.AdminServerPassword, protocol.AdminSetServerPassword:
		// Administrator permission alone never grants control over admission.
		if !actor.Owner {
			return response, nil, ErrPermissionDenied
		}
		if request.Operation == protocol.AdminSetServerPassword {
			if err := a.store.SetServerPassword(ctx, actor.auditName(), request.Password); err != nil {
				log.Printf("save server password failed: actor=%s error=%v", actor.auditName(), err)
				return response, nil, errors.New("server-password-save-failed")
			}
			if err := a.SetPassword(request.Password); err != nil {
				return response, nil, err
			}
		}
		a.mu.Lock()
		if a.password != "" {
			response.Next = 1
		}
		a.mu.Unlock()
		return response, nil, nil
	case protocol.AdminAccounts:
		accounts, err := a.admin.Accounts(ctx, actor, int64(request.Cursor))
		if err != nil {
			return response, nil, err
		}
		online := hub.onlineUsers()
		budget := protocol.AdminResponseBudget
		for _, account := range accounts {
			wire := adminAccountWire(account, online[account.ID])
			if budget -= protocol.AdminAccountSize(wire); budget < 0 {
				break
			}
			response.Accounts = append(response.Accounts, wire)
		}
		if len(response.Accounts) > 0 && (len(response.Accounts) < len(accounts) || len(accounts) == adminPageRows) {
			response.Next = response.Accounts[len(response.Accounts)-1].ID
		}
		return response, nil, nil
	case protocol.AdminAudit:
		entries, err := a.admin.Audit(ctx, actor, int64(request.Cursor))
		if err != nil {
			return response, nil, err
		}
		budget := protocol.AdminResponseBudget
		for _, entry := range entries {
			wire := adminAuditWire(entry)
			if budget -= protocol.AdminAuditEntrySize(wire); budget < 0 {
				break
			}
			response.Audit = append(response.Audit, wire)
		}
		if len(response.Audit) > 0 && (len(response.Audit) < len(entries) || len(entries) == adminPageRows) {
			response.Next = response.Audit[len(response.Audit)-1].ID
		}
		return response, nil, nil
	case protocol.AdminSetPermission:
		change, err = a.admin.SetPermission(ctx, actor, target, request.Permission, request.Enabled)
	case protocol.AdminSetJoinLevel:
		change, err = a.admin.SetJoinLevel(ctx, actor, target, request.Level)
	case protocol.AdminSetBan:
		change, err = a.admin.SetBan(ctx, actor, target, request.Enabled, request.Reason)
	case protocol.AdminSetChannelJoinLevel:
		change.Removed, err = a.admin.SetChannelJoinLevel(ctx, actor, domain.ChannelID(request.TargetID), request.Level)
		return response, change.Removed, err
	case protocol.AdminCreateChannel:
		created, err := a.admin.CreateChannel(ctx, actor, adminChannelDomain(0, request.Channel))
		response.Next = uint64(created.ID)
		return response, nil, err
	case protocol.AdminUpdateChannel:
		updated, removed, err := a.admin.UpdateChannel(ctx, actor, adminChannelDomain(domain.ChannelID(request.TargetID), request.Channel))
		response.Next = uint64(updated.ID)
		return response, removed, err
	case protocol.AdminDeleteAccount:
		change, err = a.admin.DeleteAccount(ctx, actor, target)
		response.Next = request.TargetID
		return response, change.Removed, err
	case protocol.AdminDeleteChannel:
		err = a.admin.DeleteChannel(ctx, actor, domain.ChannelID(request.TargetID))
		response.Next = request.TargetID
		return response, nil, err
	default:
		return response, nil, errAdminUnavailable
	}
	if err != nil {
		return response, change.Removed, err
	}
	online := hub.onlineUsers()
	response.Accounts = []protocol.AdminAccount{adminAccountWire(change.Account, online[change.Account.ID])}
	return response, change.Removed, nil
}

// adminChannelDomain builds a permanent channel with the fixed PCM format of
// the client pipeline from the editable settings of a request.
func adminChannelDomain(id domain.ChannelID, c protocol.AdminChannel) domain.Channel {
	audio := domain.DefaultAudioProfile()
	audio.Bitrate = c.Bitrate
	audio.Application = domain.OpusApplication(c.Application)
	return domain.Channel{
		ID:           id,
		ParentID:     domain.ChannelID(c.ParentID),
		Name:         c.Name,
		Topic:        c.Topic,
		Description:  c.Description,
		Position:     c.Position,
		MaxUsers:     c.MaxUsers,
		MinJoinLevel: c.MinJoinLevel,
		Type:         domain.ChannelTypePermanent,
		Audio:        audio,
	}
}
