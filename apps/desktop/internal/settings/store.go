package clientsettings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"uniclog.io/sonoryx/internal/fileops"
	"uniclog.io/sonoryx/internal/soundprefs"
)

const version = 1

// Settings contains desktop client preferences that survive application restarts.
type Settings struct {
	VoiceUplinkReserveKiB int               `json:"voiceUplinkReserveKiB,omitempty"`
	DisplayName           string            `json:"displayName"`
	CaptureDeviceID       string            `json:"captureDeviceId"`
	PlaybackDeviceID      string            `json:"playbackDeviceId"`
	Deafened              bool              `json:"deafened"`
	RNNoiseEnabled        bool              `json:"rnnoiseEnabled"`
	RNNoiseSensitivity    float32           `json:"rnnoiseSensitivity"`
	MicrophoneGain        float32           `json:"microphoneGain"`
	VADEnabled            bool              `json:"vadEnabled"`
	VADMode               string            `json:"vadMode"`
	VADSensitivity        float32           `json:"vadSensitivity"`
	TrustedMediaKeys      map[string]string `json:"trustedMediaKeys,omitempty"`
	Theme                 string            `json:"theme,omitempty"`
	// Locale is the interface language: 2–8 lowercase letters, for example ru, en, ky.
	// Empty means Russian. Unknown values are kept only if they match that shape,
	// so a new language does not require a settings change. Version stays 1.
	Locale       string  `json:"locale,omitempty"`
	CloseToTray  bool    `json:"closeToTray"`
	SoundPack    string  `json:"soundPack,omitempty"`
	SoundVolume  float32 `json:"soundVolume"`
	MuteOnAway   bool    `json:"muteOnAway"`
	DeafenOnAway bool    `json:"deafenOnAway"`
	// SoundCues lists button cues the user turned off. A missing id stays on.
	SoundCues     map[string]bool `json:"soundCues,omitempty"`
	RecentServers []RecentServer  `json:"recentServers,omitempty"`
	// Hotkeys maps an action to a complete physical-key shortcut.
	// A missing or invalid entry falls back to the action's default key.
	Hotkeys map[string]string `json:"hotkeys,omitempty"`
	// HotkeyNotifications shows an application popup when a hotkey acts
	// while the window is hidden. A missing field means on.
	HotkeyNotifications bool `json:"hotkeyNotifications"`
	// HotkeysEnabled arms the shortcuts. A missing field means off, so an
	// update does not start capturing keys that the user never turned on.
	HotkeysEnabled bool `json:"hotkeysEnabled"`
	// RememberParticipantVolumes restores participant volumes on the next
	// connection. A missing field means on.
	RememberParticipantVolumes bool `json:"rememberParticipantVolumes"`
	// ParticipantVolumes maps "server|local user id" to the volume of each
	// remote user id. Zero is a muted participant; the default volume is absent.
	ParticipantVolumes map[string]map[string]float32 `json:"participantVolumes,omitempty"`
}

type RecentServer struct {
	Address     string `json:"address"`
	Alias       string `json:"alias,omitempty"`
	Version     string `json:"version,omitempty"`
	Favorite    bool   `json:"favorite"`
	LastVisited int64  `json:"lastVisited"`
}

const (
	// ThemeSystem is the legacy unset value. The desktop UI treats it as ThemeDark.
	ThemeSystem = "system"
	ThemeLight  = "light"
	ThemeDark   = "dark"
	ThemeNavy   = "navy"
	ThemeBeige  = "beige"
	ThemeBrown  = "brown"
	ThemePurple = "purple"

	// themeLegacyNeutral was the grey dark theme; the navy one took its place.
	themeLegacyNeutral = "neutral"
)

// Global hotkey actions.
const (
	HotkeyMute   = "mute"
	HotkeyDeafen = "deafen"
	HotkeyAway   = "away"
)

// HotkeyActions is the stable order of actions that have a global hotkey.
var HotkeyActions = []string{HotkeyMute, HotkeyDeafen, HotkeyAway}

var defaultHotkeys = map[string]string{
	HotkeyMute:   "Ctrl+Alt+M",
	HotkeyDeafen: "Ctrl+Alt+D",
	HotkeyAway:   "Ctrl+Alt+E",
}

func KnownHotkeyAction(action string) bool {
	_, ok := defaultHotkeys[action]
	return ok
}

// NormalizeHotkeyKey canonicalizes optional modifiers and a physical key.
// Supported keys are A-Z, top-row digits and F1-F12.
func NormalizeHotkeyKey(value string) (string, bool) {
	parts := strings.Split(strings.ToUpper(strings.TrimSpace(value)), "+")
	modifiers := make(map[string]bool)
	for _, part := range parts[:len(parts)-1] {
		switch part {
		case "CTRL", "ALT", "SHIFT", "WIN":
			if modifiers[part] {
				return "", false
			}
			modifiers[part] = true
		default:
			return "", false
		}
	}
	key := parts[len(parts)-1]
	valid := len(key) == 1 && ((key[0] >= 'A' && key[0] <= 'Z') || (key[0] >= '0' && key[0] <= '9'))
	if len(key) > 1 && strings.HasPrefix(key, "F") {
		number, err := strconv.Atoi(key[1:])
		valid = err == nil && number >= 1 && number <= 12 && key == "F"+strconv.Itoa(number)
	}
	if !valid {
		return "", false
	}
	result := make([]string, 0, 5)
	for _, modifier := range []struct{ name, label string }{{"CTRL", "Ctrl"}, {"ALT", "Alt"}, {"SHIFT", "Shift"}, {"WIN", "Win"}} {
		if modifiers[modifier.name] {
			result = append(result, modifier.label)
		}
	}
	return strings.Join(append(result, key), "+"), true
}

// NormalizeHotkeys returns a key for every action, using the default where
// the stored value is missing or invalid.
func NormalizeHotkeys(keys map[string]string) map[string]string {
	out := make(map[string]string, len(HotkeyActions))
	for _, action := range HotkeyActions {
		key, ok := NormalizeHotkeyKey(keys[action])
		if !ok {
			key = defaultHotkeys[action]
		}
		out[action] = key
	}
	return out
}

// NormalizeLocale accepts an empty value as Russian and any 2–8 letter language id.
// The boolean is false when the value is not a language id.
func NormalizeLocale(value string) (string, bool) {
	if value == "" {
		return "ru", true
	}
	if len(value) < 2 || len(value) > 8 {
		return "", false
	}
	for _, r := range value {
		if r < 'a' || r > 'z' {
			return "", false
		}
	}
	return value, true
}

func NormalizeTheme(value string) string {
	switch value {
	case ThemeLight, ThemeDark, ThemeNavy, ThemeBeige, ThemeBrown, ThemePurple:
		return value
	case themeLegacyNeutral:
		return ThemeNavy
	default:
		return ThemeSystem
	}
}

type persistedSettings struct {
	Version      int `json:"version"`
	HotkeyFormat int `json:"hotkeyFormat"`
	Settings
}

// Store persists client preferences independently of any UI framework.
type Store struct {
	mu   sync.Mutex
	path string
}

func NewStore(path string) *Store { return &Store{path: path} }

func (store *Store) Load() (Settings, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	data, err := os.ReadFile(store.path)
	if err != nil {
		return Settings{}, err
	}
	var persisted persistedSettings
	if err := json.Unmarshal(data, &persisted); err != nil {
		return Settings{}, fmt.Errorf("decode settings: %w", err)
	}
	if persisted.Version != version {
		return Settings{}, fmt.Errorf("unsupported settings version %d", persisted.Version)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err == nil {
		if _, exists := fields["rnnoiseSensitivity"]; !exists {
			persisted.RNNoiseSensitivity = 1
		}
		if _, exists := fields["microphoneGain"]; !exists {
			persisted.MicrophoneGain = 1
		}
		if _, exists := fields["soundVolume"]; !exists {
			persisted.SoundVolume = soundprefs.DefaultSoundVolume
		}
		if _, exists := fields["muteOnAway"]; !exists {
			persisted.MuteOnAway = true
		}
		if _, exists := fields["hotkeyNotifications"]; !exists {
			persisted.HotkeyNotifications = true
		}
		if _, exists := fields["rememberParticipantVolumes"]; !exists {
			persisted.RememberParticipantVolumes = true
		}
	}
	if persisted.VoiceUplinkReserveKiB < 1 || persisted.VoiceUplinkReserveKiB > 1048576 {
		persisted.VoiceUplinkReserveKiB = 128
	}
	persisted.Theme = NormalizeTheme(persisted.Theme)
	persisted.SoundPack = soundprefs.NormalizeSoundPack(persisted.SoundPack)
	persisted.SoundCues = soundprefs.NormalizeSoundCues(persisted.SoundCues)
	if persisted.HotkeyFormat < 2 {
		for action, key := range persisted.Hotkeys {
			if len(key) == 1 {
				persisted.Hotkeys[action] = "Ctrl+Alt+" + key
			}
		}
	}
	persisted.Hotkeys = NormalizeHotkeys(persisted.Hotkeys)
	if persisted.SoundVolume < 0 || persisted.SoundVolume > 1 {
		persisted.SoundVolume = soundprefs.DefaultSoundVolume
	}
	return persisted.Settings, nil
}

func (store *Store) Save(settings Settings) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	data, err := json.MarshalIndent(persistedSettings{Version: version, HotkeyFormat: 2, Settings: settings}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(store.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".settings-*")
	if err != nil {
		return fmt.Errorf("create temporary settings: %w", err)
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	if err := fileops.PublishFile(tmp.Name(), store.path); err != nil {
		return fmt.Errorf("replace settings: %w", err)
	}
	return nil
}

func DefaultPath() (string, error) {
	configPath, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("resolve application data directory: %w", err)
	}
	return filepath.Join(configPath, "Sonoryx", "settings.json"), nil
}
