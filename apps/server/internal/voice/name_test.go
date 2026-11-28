package voice

import (
	"testing"

	"uniclog.io/sonoryx/internal/domain"
)

func TestHubRejectsControlCharactersInNames(t *testing.T) {
	for _, name := range []string{"a\x1b[2Jb", "a\rb", "a\nb", "a\tb", "a\x00b", "a\u009bb", "a\u2028b", "a\u2029b"} {
		t.Run(name, func(t *testing.T) {
			hub := NewHub()
			if _, err := hub.CreateSession(name, nil); err == nil {
				t.Fatal("accepted unsafe participant name")
			}
			session, err := hub.CreateSession("Алиса", nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := hub.Rename(session.ID, name); err == nil {
				t.Fatal("accepted unsafe rename")
			}
			if _, err := hub.CreateChannel(domain.Channel{Name: name}); err == nil {
				t.Fatal("accepted unsafe channel name")
			}
			if _, err := NewHubWithServerInfo(domain.ServerInfo{Name: name}); err == nil {
				t.Fatal("accepted unsafe server name")
			}
		})
	}
}
