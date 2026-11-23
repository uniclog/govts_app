// Package apppopup provides Sonoryx notification windows independently of the
// feature that requests them. Native popups are currently supported on Windows;
// other platforms accept notifications without displaying a window.
package apppopup

import (
	"strconv"
	"strings"
	"time"
)

// Popup owns a single silent notification window. It does not activate or take
// keyboard focus. A new message replaces the current text and restarts the
// 2.8-second dismissal timer. Clicking the window dismisses it early.
type Popup struct {
	backend backend
}

// Look is the card palette of the current interface theme as 0xRRGGBB values.
type Look struct {
	Card, Line, Text, Muted uint32
}

// New creates a hidden popup. icon is a PNG of the application mark, drawn at
// the left of the card. Call New after the application has configured DPI
// awareness, and Close it during shutdown to release its window and thread.
func New(icon []byte) (*Popup, error) {
	b, err := newBackend(icon)
	if err != nil {
		return nil, err
	}
	return &Popup{backend: b}, nil
}

// SetLook replaces the card colours used from the next message on. Safe from
// any goroutine.
func (p *Popup) SetLook(look Look) {
	p.backend.setLook(look)
}

// Show queues plain notification text without waiting for a UI thread.
// Calls are safe from multiple goroutines; only the latest pending text is kept.
func (p *Popup) Show(title, body string) error {
	return p.backend.show(Message{Title: title, Body: body})
}

// Message is one popup. Lifetime zero keeps the 2.8-second default. OnClick,
// when set, runs on its own goroutine after a click dismisses the popup.
type Message struct {
	Title, Body string
	Lifetime    time.Duration
	OnClick     func()
}

// ShowMessage queues a popup like Show, with its own lifetime and click action.
func (p *Popup) ShowMessage(message Message) error {
	return p.backend.show(message)
}

// Close releases the popup's resources. Repeated calls are harmless.
func (p *Popup) Close() {
	p.backend.close()
}

// ParseLook reads #rrggbb values from the interface. Missing or invalid keys
// keep the dark theme colours (darkPalette in appearance.ts).
func ParseLook(colors map[string]string) Look {
	return Look{
		Card:  parseHex(colors["card"], 0x0c1d30),
		Line:  parseHex(colors["line"], 0x203b54),
		Text:  parseHex(colors["text"], 0xdce7f3),
		Muted: parseHex(colors["muted"], 0x89a1b9),
	}
}

func parseHex(value string, fallback uint32) uint32 {
	text := strings.TrimSpace(value)
	if len(text) != 7 || text[0] != '#' {
		return fallback
	}
	n, err := strconv.ParseUint(text[1:], 16, 32)
	if err != nil {
		return fallback
	}
	return uint32(n)
}

type backend interface {
	show(Message) error
	setLook(Look)
	close()
}
