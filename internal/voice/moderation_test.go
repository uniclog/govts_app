package voice

import (
	"errors"
	"net"
	"testing"

	"uniclog.io/sonoryx/internal/domain"
	"uniclog.io/sonoryx/internal/persist"
)

func TestDragAllowsAnotherSessionOfSameAccount(t *testing.T) {
	hub := NewHub()
	actor, _, err := hub.CreateAuthenticatedSession(
		"alice-desktop",
		&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5001},
		7,
		0,
		persist.PermissionDrag,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := hub.CreateAuthenticatedSession(
		"alice-laptop",
		&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5002},
		7,
		0,
		persist.PermissionDrag,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := hub.Drag(actor.ID, target.ID, DefaultChannelID); err != nil {
		t.Fatalf("Drag() same-account error = %v", err)
	}
	stored, ok := hub.Get(target.ID)
	if !ok || stored.ChannelID != DefaultChannelID {
		t.Fatalf("dragged session = %+v, found=%t", stored, ok)
	}
	other := mustCreateChannel(t, hub, domain.Channel{Name: "other"})
	if _, err := hub.Drag(actor.ID, actor.ID, other.ID); err != nil {
		t.Fatalf("Drag() own session error = %v", err)
	}
	stored, ok = hub.Get(actor.ID)
	if !ok || stored.ChannelID != other.ID {
		t.Fatalf("self-dragged session = %+v, found=%t", stored, ok)
	}
}

func TestSameAccountCannotKickOrBanItselfAndForeignOwnerStaysProtected(t *testing.T) {
	hub := NewHub()
	actor, _, err := hub.CreateAuthenticatedSession(
		"alice-desktop",
		&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5101},
		7,
		0,
		persist.PermissionKick|persist.PermissionBan|persist.PermissionDrag,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	sameAccount, _, err := hub.CreateAuthenticatedSession(
		"alice-laptop",
		&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5102},
		7,
		0,
		0,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	foreignOwner, _, err := hub.CreateAuthenticatedSession(
		"owner",
		&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5103},
		8,
		0,
		0,
		true,
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := hub.CheckModeration(actor.ID, sameAccount.ID, persist.PermissionKick); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("same-account kick check error = %v, want %v", err, ErrPermissionDenied)
	}
	if _, err := hub.CheckModeration(actor.ID, sameAccount.ID, persist.PermissionBan); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("same-account ban check error = %v, want %v", err, ErrPermissionDenied)
	}
	if _, err := hub.CheckDrag(actor.ID, foreignOwner.ID, DefaultChannelID); !errors.Is(err, ErrPermissionDenied) {
		t.Fatalf("foreign owner drag check error = %v, want %v", err, ErrPermissionDenied)
	}
}
