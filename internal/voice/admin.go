package voice

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log"
	"strings"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/persist"
	"uniclog.io/sonoryx/internal/protocol"
)

// adminPageRows bounds one store read for a list page; the datagram budget
// usually ends the page first.
const adminPageRows = 32

// AdminActor is who performs an administration operation. Local is the server
// console: it has every right and passes no hierarchy checks.
type AdminActor struct {
	Local       bool
	UserID      int64
	JoinLevel   uint16
	Permissions uint8
	Owner       bool
}

// ConsoleAdmin is the actor of local console commands.
var ConsoleAdmin = AdminActor{Local: true}

func adminActorForSession(session Session) AdminActor {
	return AdminActor{UserID: session.UserID, JoinLevel: session.JoinLevel, Permissions: session.Permissions, Owner: session.Owner}
}

func (a AdminActor) privileged() bool { return a.Local || a.Owner }

func (a AdminActor) allowed() bool {
	return a.privileged() || a.Permissions&persist.PermissionAdmin != 0
}

func (a AdminActor) auditName() string {
	if a.Local {
		return persist.ConsoleActor
	}
	return fmt.Sprint(a.UserID)
}

// AdminChange is what an applied mutation did to the live server.
type AdminChange struct {
	Account persist.Account
	// Removed lists sessions dropped by a ban or a raised join level; the
	// caller releases their transport state.
	Removed []uint64
}

// Administration applies account and channel policy for the console and for
// remote administrators. Callers hold the policy gate.
type Administration struct {
	store  *persist.Store
	hub    *Hub
	notify func(userID int64, level uint16, privileges uint8)
}

func NewAdministration(store *persist.Store, hub *Hub) *Administration {
	return &Administration{store: store, hub: hub}
}

// SetPrivilegeNotifier is called after an account's level or permissions change
// so its connected clients can update their controls.
func (a *Administration) SetPrivilegeNotifier(notify func(userID int64, level uint16, privileges uint8)) {
	a.notify = notify
}

// checkTarget enforces the general account hierarchy: remote actors cannot
// change an owner or themselves, and only an owner changes another admin.
// Permission mutations use checkPermissionTarget for the owner self exception.
func (a *Administration) checkTarget(actor AdminActor, target persist.Account) error {
	if actor.Local {
		return nil
	}
	if target.ID == actor.UserID || target.Owner {
		return ErrPermissionDenied
	}
	if !actor.Owner && (target.Permissions&persist.PermissionAdmin != 0 || target.JoinLevel > actor.JoinLevel) {
		return ErrPermissionDenied
	}
	return nil
}

// checkPermissionTarget lets an owner edit their own permission bits and file
// rights. Other mutations retain checkTarget's protection against self-editing.
func (a *Administration) checkPermissionTarget(actor AdminActor, target persist.Account) error {
	if actor.Owner && target.Owner && target.ID == actor.UserID {
		return nil
	}
	return a.checkTarget(actor, target)
}

func (a *Administration) account(ctx context.Context, id int64) (persist.Account, error) {
	account, err := a.store.Account(ctx, id)
	if err != nil {
		return persist.Account{}, fmt.Errorf("account %d not found", id)
	}
	return account, nil
}

func (a *Administration) Accounts(ctx context.Context, actor AdminActor, afterID int64) ([]persist.Account, error) {
	if !actor.allowed() {
		return nil, ErrPermissionDenied
	}
	return a.store.AccountsPage(ctx, afterID, adminPageRows)
}

// SetFileRights uses the same hierarchy and external policy gate as other account mutations.
func (a *Administration) SetFileRights(ctx context.Context, actor AdminActor, id int64, upload, download bool) error {
	if !actor.allowed() {
		return ErrPermissionDenied
	}
	target, err := a.account(ctx, id)
	if err != nil {
		return err
	}
	if err = a.checkPermissionTarget(actor, target); err != nil {
		return err
	}
	return a.store.SetFileRights(ctx, actor.auditName(), id, upload, download)
}

func (a *Administration) DeleteAttachment(ctx context.Context, actor AdminActor, id int64) error {
	if !actor.allowed() {
		return ErrPermissionDenied
	}
	attachment, err := a.store.FileAttachment(ctx, id)
	if err != nil {
		return err
	}
	if !actor.Local && attachment.UserID != actor.UserID {
		target, err := a.account(ctx, attachment.UserID)
		if err != nil {
			return err
		}
		if err = a.checkTarget(actor, target); err != nil {
			return err
		}
	}
	return a.store.DeleteFile(ctx, actor.auditName(), id)
}

func (a *Administration) Audit(ctx context.Context, actor AdminActor, beforeID int64) ([]persist.AuditEntry, error) {
	if !actor.allowed() {
		return nil, ErrPermissionDenied
	}
	return a.store.AuditPage(ctx, beforeID, adminPageRows)
}

func (a *Administration) SetPermission(ctx context.Context, actor AdminActor, id int64, permission uint8, enabled bool) (AdminChange, error) {
	if !actor.allowed() || !persist.ValidPermission(permission) {
		return AdminChange{}, ErrPermissionDenied
	}
	// Only the owner appoints administrators; others grant what they hold.
	if !actor.privileged() && (permission == persist.PermissionAdmin || actor.Permissions&permission == 0) {
		return AdminChange{}, ErrPermissionDenied
	}
	target, err := a.account(ctx, id)
	if err != nil {
		return AdminChange{}, err
	}
	if err := a.checkPermissionTarget(actor, target); err != nil {
		return AdminChange{}, err
	}
	account, err := a.store.SetPermissionBy(ctx, actor.auditName(), id, permission, enabled)
	if err != nil {
		return AdminChange{}, err
	}
	a.hub.ApplyPermissions(id, account.Permissions)
	a.notifyAccount(account)
	return AdminChange{Account: account}, nil
}

func (a *Administration) SetJoinLevel(ctx context.Context, actor AdminActor, id int64, level uint16) (AdminChange, error) {
	if !actor.allowed() || (!actor.privileged() && level > actor.JoinLevel) {
		return AdminChange{}, ErrPermissionDenied
	}
	target, err := a.account(ctx, id)
	if err != nil {
		return AdminChange{}, err
	}
	if err := a.checkTarget(actor, target); err != nil {
		return AdminChange{}, err
	}
	if err := a.store.SetJoinLevelBy(ctx, actor.auditName(), id, level); err != nil {
		return AdminChange{}, fmt.Errorf("set join level: %w", err)
	}
	removed := a.hub.ApplyJoinLevel(id, level)
	account, err := a.store.Account(ctx, id)
	if err != nil {
		return AdminChange{Removed: removed}, err
	}
	a.notifyAccount(account)
	return AdminChange{Account: account, Removed: removed}, nil
}

func (a *Administration) SetBan(ctx context.Context, actor AdminActor, id int64, banned bool, reason string) (AdminChange, error) {
	if !actor.allowed() || (!actor.privileged() && actor.Permissions&persist.PermissionBan == 0) {
		return AdminChange{}, ErrPermissionDenied
	}
	target, err := a.account(ctx, id)
	if err != nil {
		return AdminChange{}, err
	}
	if err := a.checkTarget(actor, target); err != nil {
		return AdminChange{}, err
	}
	reason = strings.TrimSpace(reason)
	if !banned {
		reason = ""
	} else if reason == "" {
		reason = "administrator action"
	}
	if err := a.store.SetBan(ctx, actor.auditName(), id, banned, reason); err != nil {
		return AdminChange{}, err
	}
	var removed []uint64
	if banned {
		removed = a.hub.RemoveUserSessions(id)
	}
	account, err := a.store.Account(ctx, id)
	return AdminChange{Account: account, Removed: removed}, err
}

// DeleteAccount forgets an account and disconnects its sessions; its key
// registers a new account on the next connection. Like a ban, it needs the
// ban permission unless the actor is the owner.
func (a *Administration) DeleteAccount(ctx context.Context, actor AdminActor, id int64) (AdminChange, error) {
	if !actor.allowed() || (!actor.privileged() && actor.Permissions&persist.PermissionBan == 0) {
		return AdminChange{}, ErrPermissionDenied
	}
	target, err := a.account(ctx, id)
	if err != nil {
		return AdminChange{}, err
	}
	if err := a.checkTarget(actor, target); err != nil {
		return AdminChange{}, err
	}
	if err := a.store.DeleteAccountBy(ctx, actor.auditName(), id); err != nil {
		return AdminChange{}, err
	}
	return AdminChange{Removed: a.hub.RemoveUserSessions(id)}, nil
}

func (a *Administration) SetChannelJoinLevel(ctx context.Context, actor AdminActor, channelID domain.ChannelID, level uint16) ([]uint64, error) {
	if !actor.allowed() {
		return nil, ErrPermissionDenied
	}
	channel, ok := a.hub.GetChannel(channelID)
	if !ok {
		return nil, fmt.Errorf("channel %d not found", channelID)
	}
	// A non-owner administrator cannot lock a channel above their own level
	// or unlock one that is already above it.
	if !actor.privileged() && (level > actor.JoinLevel || channel.MinJoinLevel > actor.JoinLevel) {
		return nil, ErrPermissionDenied
	}
	if err := a.store.SetChannelJoinLevelBy(ctx, actor.auditName(), channelID, level); err != nil {
		return nil, err
	}
	removed, err := a.hub.SetChannelJoinLevel(channelID, level)
	if err != nil {
		return nil, fmt.Errorf("saved channel threshold but could not apply it: %w", err)
	}
	return removed, nil
}

// CreateChannel stores and announces a new channel. A non-owner administrator
// cannot lock it above their own level.
func (a *Administration) CreateChannel(ctx context.Context, actor AdminActor, channel domain.Channel) (domain.Channel, error) {
	if !actor.allowed() || (!actor.privileged() && channel.MinJoinLevel > actor.JoinLevel) {
		return domain.Channel{}, ErrPermissionDenied
	}
	prepared, err := a.hub.PrepareChannel(channel)
	if err != nil {
		return domain.Channel{}, err
	}
	if err := a.store.InsertChannelBy(ctx, actor.auditName(), prepared); err != nil {
		return domain.Channel{}, err
	}
	if err := a.hub.AddChannel(prepared); err != nil {
		return domain.Channel{}, fmt.Errorf("saved channel but could not add it: %w", err)
	}
	return prepared, nil
}

// UpdateChannel replaces the settings of a channel, including its parent and
// position. A non-owner administrator can change only channels at or below
// their level and cannot lock one above it.
func (a *Administration) UpdateChannel(ctx context.Context, actor AdminActor, channel domain.Channel) (domain.Channel, []uint64, error) {
	if !actor.allowed() {
		return domain.Channel{}, nil, ErrPermissionDenied
	}
	current, next, err := a.hub.CheckChannelUpdate(channel)
	if err != nil {
		return domain.Channel{}, nil, err
	}
	if !actor.privileged() && (current.MinJoinLevel > actor.JoinLevel || next.MinJoinLevel > actor.JoinLevel) {
		return domain.Channel{}, nil, ErrPermissionDenied
	}
	if next == current {
		return next, nil, nil
	}
	if err := a.store.UpdateChannelBy(ctx, actor.auditName(), next); err != nil {
		return domain.Channel{}, nil, err
	}
	removed, err := a.hub.UpdateChannel(next)
	if err != nil {
		return domain.Channel{}, nil, fmt.Errorf("saved channel but could not apply it: %w", err)
	}
	return next, removed, nil
}

// DeleteChannel removes a channel without subchannels and moves its members
// to the default channel. A non-owner administrator can delete only channels
// at or below their level.
func (a *Administration) DeleteChannel(ctx context.Context, actor AdminActor, id domain.ChannelID) error {
	if !actor.allowed() {
		return ErrPermissionDenied
	}
	channel, err := a.hub.CheckChannelDelete(id)
	if err != nil {
		return err
	}
	if !actor.privileged() && channel.MinJoinLevel > actor.JoinLevel {
		return ErrPermissionDenied
	}
	if err := a.store.DeleteChannelBy(ctx, actor.auditName(), id); err != nil {
		return err
	}
	if _, err := a.hub.DeleteChannel(id); err != nil {
		return fmt.Errorf("deleted stored channel but could not remove it: %w", err)
	}
	return nil
}

func (a *Administration) notifyAccount(account persist.Account) {
	if a.notify != nil {
		a.notify(account.ID, account.JoinLevel, account.Privileges())
	}
}

// adminAccountWire converts a stored account for PacketAdminAck.
func adminAccountWire(account persist.Account, online bool) protocol.AdminAccount {
	sum := sha256.Sum256(account.PublicKey[:])
	wire := protocol.AdminAccount{
		ID:          uint64(account.ID),
		JoinLevel:   account.JoinLevel,
		Permissions: account.Permissions,
		Name:        protocol.TruncateUTF8(account.DisplayName, domain.MaxParticipantNameBytes),
		BanReason:   protocol.TruncateUTF8(account.BanReason, protocol.MaxAdminReasonBytes),
	}
	copy(wire.Fingerprint[:], sum[:8])
	if account.Banned {
		wire.Flags |= protocol.AdminAccountBanned
	}
	if account.Owner {
		wire.Flags |= protocol.AdminAccountOwner
	}
	if online {
		wire.Flags |= protocol.AdminAccountOnline
	}
	return wire
}

func adminAuditWire(entry persist.AuditEntry) protocol.AdminAuditEntry {
	var target uint64
	if entry.TargetUserID > 0 {
		target = uint64(entry.TargetUserID)
	}
	return protocol.AdminAuditEntry{
		ID:           uint64(entry.ID),
		HappenedAtMS: entry.HappenedAt.UnixMilli(),
		TargetUserID: target,
		Actor:        protocol.TruncateUTF8(entry.Actor, protocol.MaxAdminActorBytes),
		Action:       protocol.TruncateUTF8(entry.Action, protocol.MaxAdminActionBytes),
		Detail:       protocol.TruncateUTF8(entry.Detail, protocol.MaxAdminDetailBytes),
	}
}

// onlineUsers reports which accounts have a live session.
func (h *Hub) onlineUsers() map[int64]bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	online := make(map[int64]bool, len(h.sessions))
	for _, session := range h.sessions {
		if session.UserID > 0 {
			online[session.UserID] = true
		}
	}
	return online
}

var errAdminUnavailable = errors.New("administration is unavailable")

func logAdminChange(actor AdminActor, operation protocol.AdminOperation, target uint64, err error) {
	log.Printf("admin request: actor=%s operation=%d target=%d error=%v", actor.auditName(), operation, target, err)
}
