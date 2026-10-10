package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/persist"
)

func accountFingerprint(key [32]byte) string {
	sum := sha256.Sum256(key[:])
	return hex.EncodeToString(sum[:8])
}

func (console *Console) writeAccounts() error {
	if console.store == nil {
		return console.writeStoreUnavailable()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	accounts, err := console.store.Accounts(ctx)
	if err != nil {
		return err
	}
	for _, account := range accounts {
		if _, err := fmt.Fprintf(console.output, "id=%d name=%q key=%s join_level=%d banned=%t\n", account.ID, account.DisplayName, accountFingerprint(account.PublicKey), account.JoinLevel, account.Banned); err != nil {
			return err
		}
	}
	return nil
}

func (console *Console) executeAccount(argument string) error {
	if console.store == nil {
		return console.writeStoreUnavailable()
	}
	if console.policyGate != nil {
		console.policyGate.Lock()
		defer console.policyGate.Unlock()
	}
	fields := strings.Fields(argument)
	if len(fields) == 2 && fields[0] == "set-owner" {
		id, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || id <= 0 {
			return console.writeUsage("account set-owner <user-id>")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := console.store.SetOwner(ctx, id); err != nil {
			return err
		}
		console.hub.ApplyOwner(id)
		_, err = fmt.Fprintf(console.output, "account %d owner=true\n", id)
		return err
	}
	if len(fields) == 4 && fields[0] == "set-permission" {
		id, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || id <= 0 {
			return console.writeUsage("account set-permission <user-id> <kick|ban|drag> <on|off>")
		}
		var permission uint8
		switch fields[2] {
		case "kick":
			permission = persist.PermissionKick
		case "ban":
			permission = persist.PermissionBan
		case "drag":
			permission = persist.PermissionDrag
		default:
			return console.writeUsage("account set-permission <user-id> <kick|ban|drag> <on|off>")
		}
		if fields[3] != "on" && fields[3] != "off" {
			return console.writeUsage("account set-permission <user-id> <kick|ban|drag> <on|off>")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		account, err := console.store.SetPermission(ctx, id, permission, fields[3] == "on")
		if err != nil {
			return err
		}
		console.hub.ApplyPermissions(id, account.Permissions)
		if console.notifyPrivileges != nil {
			console.notifyPrivileges(id, account.JoinLevel, account.Permissions)
		}
		_, err = fmt.Fprintf(console.output, "account %d permissions=%d\n", id, account.Permissions)
		return err
	}
	if len(fields) == 2 && (fields[0] == "ban" || fields[0] == "unban") {
		id, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || id <= 0 {
			return console.writeUsage("account <ban|unban> <user-id>")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := console.store.SetBan(ctx, "local-console", id, fields[0] == "ban", "local console"); err != nil {
			return err
		}
		if fields[0] == "ban" {
			console.hub.RemoveUserSessions(id)
		}
		_, err = fmt.Fprintf(console.output, "account %d banned=%t\n", id, fields[0] == "ban")
		return err
	}
	if len(fields) == 3 && fields[0] == "set-join-level" {
		id, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || id <= 0 {
			return console.writeUsage("account set-join-level <user-id> <level>")
		}
		level, err := strconv.ParseUint(fields[2], 10, 16)
		if err != nil {
			return console.writeUsage("account set-join-level <user-id> <level>")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := console.store.SetJoinLevel(ctx, id, uint16(level)); err != nil {
			return fmt.Errorf("set join level: %w", err)
		}
		console.hub.ApplyJoinLevel(id, uint16(level))
		if console.notifyPrivileges != nil {
			account, err := console.store.Account(ctx, id)
			if err == nil {
				console.notifyPrivileges(id, account.JoinLevel, account.Permissions)
			}
		}
		_, err = fmt.Fprintf(console.output, "account %d join_level=%d\n", id, level)
		return err
	}
	if len(fields) != 1 {
		return console.writeUsage("account <user-id> | account set-join-level <user-id> <level>")
	}
	id, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || id <= 0 {
		return console.writeUsage("account <user-id>")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	account, err := console.store.Account(ctx, id)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(console.output, "id=%d name=%q key=%s join_level=%d permissions=%d banned=%t owner=%t\n", account.ID, account.DisplayName, accountFingerprint(account.PublicKey), account.JoinLevel, account.Permissions, account.Banned, account.Owner)
	return err
}

func (console *Console) setChannelJoinLevel(argument string) error {
	if console.store == nil {
		return console.writeStoreUnavailable()
	}
	if console.policyGate != nil {
		console.policyGate.Lock()
		defer console.policyGate.Unlock()
	}
	fields := strings.Fields(argument)
	if len(fields) != 2 {
		return console.writeUsage("channel set-min-join-level <channel-id> <level>")
	}
	id, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil || id == 0 {
		return console.writeUsage("channel set-min-join-level <channel-id> <level>")
	}
	level, err := strconv.ParseUint(fields[1], 10, 16)
	if err != nil {
		return console.writeUsage("channel set-min-join-level <channel-id> <level>")
	}
	if _, ok := console.hub.GetChannel(domain.ChannelID(id)); !ok {
		return fmt.Errorf("channel %d not found", id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := console.store.SetChannelJoinLevel(ctx, domain.ChannelID(id), uint16(level)); err != nil {
		return err
	}
	if _, err := console.hub.SetChannelJoinLevel(domain.ChannelID(id), uint16(level)); err != nil {
		return fmt.Errorf("saved channel threshold but could not apply it: %w", err)
	}
	_, err = fmt.Fprintf(console.output, "channel %d min_join_level=%d\n", id, level)
	return err
}

func (console *Console) writeStoreUnavailable() error {
	_, err := fmt.Fprintln(console.output, "error: account database is unavailable")
	return err
}
