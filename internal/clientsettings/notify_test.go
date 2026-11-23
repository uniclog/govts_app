package clientsettings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizePresence(t *testing.T) {
	for value, want := range map[string]string{"": PresenceOnline, "online": PresenceOnline, "invisible": PresenceInvisible, "away": PresenceOnline} {
		if got := NormalizePresence(value); got != want {
			t.Errorf("NormalizePresence(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestNotificationSettingsRoundTrip(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "settings.json"))
	saved := Settings{
		Presence:           PresenceInvisible,
		NotificationsMuted: true,
		RecentServers:      []RecentServer{{Address: "127.0.0.1:9000", Subscribed: true, Name: "ReCon", WatchSecret: "dpapi:AAAA"}},
		Hotkeys:            map[string]string{HotkeyNotifications: "Ctrl+Shift+N"},
	}
	if err := store.Save(saved); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Presence != PresenceInvisible || !loaded.NotificationsMuted {
		t.Fatalf("presence=%q muted=%t", loaded.Presence, loaded.NotificationsMuted)
	}
	if len(loaded.RecentServers) != 1 || loaded.RecentServers[0] != saved.RecentServers[0] {
		t.Fatalf("recent servers = %+v", loaded.RecentServers)
	}
	if loaded.Hotkeys[HotkeyNotifications] != "Ctrl+Shift+N" {
		t.Fatalf("notification hotkey = %q", loaded.Hotkeys[HotkeyNotifications])
	}
}

// Settings written before notifications have no presence and no
// notification hotkey: the client is online and gets the default key.
func TestOlderSettingsGetNotificationDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"hotkeyFormat":2,"hotkeys":{"mute":"Ctrl+Alt+M"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := NewStore(path).Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Presence != PresenceOnline || loaded.NotificationsMuted {
		t.Fatalf("presence=%q muted=%t", loaded.Presence, loaded.NotificationsMuted)
	}
	if loaded.Hotkeys[HotkeyNotifications] != "Ctrl+Alt+N" {
		t.Fatalf("notification hotkey = %q, want Ctrl+Alt+N", loaded.Hotkeys[HotkeyNotifications])
	}
	if !KnownHotkeyAction(HotkeyNotifications) {
		t.Fatal("notifications is not a hotkey action")
	}
}
