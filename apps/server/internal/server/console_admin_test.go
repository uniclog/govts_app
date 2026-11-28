package server

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"uniclog.io/sonoryx/apps/server/internal/persist"
	"uniclog.io/sonoryx/apps/server/internal/voice"
)

func TestConsoleAdminPermissionAndAudit(t *testing.T) {
	store, err := persist.Open(filepath.Join(t.TempDir(), "server.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var key [32]byte
	key[0] = 1
	account, err := store.FindOrCreateAccount(context.Background(), key, "alice")
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	console := NewConsole(voice.NewHub(), strings.NewReader(""), &output, ConsoleInfo{}, store)
	var notified []uint8
	console.SetPrivilegeNotifier(func(userID int64, _ uint16, privileges uint8) {
		if userID == account.ID {
			notified = append(notified, privileges)
		}
	})
	run := func(line string) string {
		t.Helper()
		output.Reset()
		if err := console.Execute(line); err != nil {
			t.Fatalf("Execute(%q) error = %v", line, err)
		}
		return output.String()
	}

	if help := run("help"); !strings.Contains(help, "<kick|ban|drag|admin>") || !strings.Contains(help, "audit [before-id]") {
		t.Fatalf("help = %q", help)
	}
	if got := run(fmt.Sprintf("account set-permission %d admin on", account.ID)); got != fmt.Sprintf("account %d permissions=%d\n", account.ID, persist.PermissionAdmin) {
		t.Fatalf("grant admin output = %q", got)
	}
	if len(notified) != 1 || notified[0] != persist.PermissionAdmin {
		t.Fatalf("notifications = %v", notified)
	}
	if got := run(fmt.Sprintf("account set-permission %d admin maybe", account.ID)); !strings.Contains(got, "error: usage: account set-permission") {
		t.Fatalf("bad switch output = %q", got)
	}
	if got := run(fmt.Sprintf("account ban %d", account.ID)); got != fmt.Sprintf("account %d banned=true\n", account.ID) {
		t.Fatalf("ban output = %q", got)
	}

	audit := run("audit")
	lines := strings.Split(strings.TrimSpace(audit), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `action="set-ban"`) || !strings.Contains(lines[0], `detail="local console"`) ||
		!strings.Contains(lines[1], `actor="local-console" action="set-permission"`) {
		t.Fatalf("audit = %q", audit)
	}
	var newest int64
	if _, err := fmt.Sscanf(lines[0], "id=%d", &newest); err != nil {
		t.Fatal(err)
	}
	if older := run(fmt.Sprintf("audit %d", newest)); strings.Count(older, "\n") != 1 || !strings.Contains(older, "set-permission") {
		t.Fatalf("older audit = %q", older)
	}
	if got := run("audit nope"); got != "error: usage: audit [before-id]\n" {
		t.Fatalf("bad audit cursor output = %q", got)
	}
}

func TestConsoleAuditWithoutStore(t *testing.T) {
	var output bytes.Buffer
	console := NewConsole(voice.NewHub(), strings.NewReader(""), &output, ConsoleInfo{})
	if err := console.Execute("audit"); err != nil {
		t.Fatal(err)
	}
	if output.String() != "error: account database is unavailable\n" {
		t.Fatalf("output = %q", output.String())
	}
}
