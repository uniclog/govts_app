package clientsettings

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const version = 1

// Settings contains desktop client preferences that survive application restarts.
type Settings struct {
	DisplayName        string            `json:"displayName"`
	CaptureDeviceID    string            `json:"captureDeviceId"`
	PlaybackDeviceID   string            `json:"playbackDeviceId"`
	Deafened           bool              `json:"deafened"`
	RNNoiseEnabled     bool              `json:"rnnoiseEnabled"`
	RNNoiseSensitivity float32           `json:"rnnoiseSensitivity"`
	MicrophoneGain     float32           `json:"microphoneGain"`
	VADEnabled         bool              `json:"vadEnabled"`
	VADMode            string            `json:"vadMode"`
	VADSensitivity     float32           `json:"vadSensitivity"`
	TrustedMediaKeys   map[string]string `json:"trustedMediaKeys,omitempty"`
	Theme              string            `json:"theme,omitempty"`
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
	// Hotkeys maps an action to the key pressed together with Ctrl+Alt.
	// A missing or invalid entry falls back to the action's default key.
	Hotkeys map[string]string `json:"hotkeys,omitempty"`
	// HotkeyNotifications shows a system notification when a hotkey acts
	// while the window is hidden. A missing field means on.
	HotkeyNotifications bool `json:"hotkeyNotifications"`
	// HotkeysEnabled arms the shortcuts. A missing field means off, so an
	// update does not start capturing keys that the user never turned on.
	HotkeysEnabled bool `json:"hotkeysEnabled"`
}

type RecentServer struct {
	Address     string `json:"address"`
	Alias       string `json:"alias,omitempty"`
	Favorite    bool   `json:"favorite"`
	LastVisited int64  `json:"lastVisited"`
}

const (
	// ThemeSystem is the legacy unset value. The desktop UI treats it as ThemeDark.
	ThemeSystem  = "system"
	ThemeLight   = "light"
	ThemeNeutral = "neutral"
	ThemeDark    = "dark"
	ThemeBeige   = "beige"
	ThemeBrown   = "brown"
	ThemePurple  = "purple"
)

// Notification sound packs. internal/client renders them; this package owns
// the ids so stored settings and the renderer cannot drift apart.
const (
	SoundPackShort  = "short"
	SoundPackSoft   = "soft"
	SoundPackLow    = "low"
	SoundPackDouble = "double"
	SoundPackSignal = "signal"

	DefaultSoundPack   = SoundPackSoft
	DefaultSoundVolume = float32(0.8)

	// Button cues. internal/client maps each one to a notification kind.
	// Absent in SoundCues means the cue is on.
	CueMicOff      = "mic-off"
	CueMicOn       = "mic-on"
	CueSpeakerOff  = "speaker-off"
	CueSpeakerOn   = "speaker-on"
	CueAway        = "away"
	CueReturned    = "returned"
	CueMessageSent = "message-sent"
	CueScreenStart = "screen-start"
	CueScreenStop  = "screen-stop"
	CueChannel     = "channel"
)

// ButtonCues is the stable order of sounds a button can play.
var ButtonCues = []string{
	CueMicOff,
	CueMicOn,
	CueSpeakerOff,
	CueSpeakerOn,
	CueAway,
	CueReturned,
	CueMessageSent,
	CueScreenStart,
	CueScreenStop,
	CueChannel,
}

func KnownCue(id string) bool {
	for _, cue := range ButtonCues {
		if cue == id {
			return true
		}
	}
	return false
}

// SoundCueEnabled reports whether a button cue should play.
// An unknown id is not a button cue and stays on.
func SoundCueEnabled(cues map[string]bool, id string) bool {
	if id == "" || !KnownCue(id) {
		return true
	}
	if cues == nil {
		return true
	}
	enabled, ok := cues[id]
	return !ok || enabled
}

// NormalizeSoundCues keeps only button cues that are explicitly off.
func NormalizeSoundCues(cues map[string]bool) map[string]bool {
	if len(cues) == 0 {
		return nil
	}
	out := make(map[string]bool)
	for _, id := range ButtonCues {
		if enabled, ok := cues[id]; ok && !enabled {
			out[id] = false
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SoundCueStates is every button cue, on unless the stored map turns it off.
func SoundCueStates(cues map[string]bool) map[string]bool {
	out := make(map[string]bool, len(ButtonCues))
	for _, id := range ButtonCues {
		out[id] = SoundCueEnabled(cues, id)
	}
	return out
}

// Global hotkey actions. Each one is Ctrl+Alt plus a single key.
const (
	HotkeyMute   = "mute"
	HotkeyDeafen = "deafen"
	HotkeyAway   = "away"
)

// HotkeyActions is the stable order of actions that have a global hotkey.
var HotkeyActions = []string{HotkeyMute, HotkeyDeafen, HotkeyAway}

var defaultHotkeys = map[string]string{
	HotkeyMute:   "M",
	HotkeyDeafen: "D",
	HotkeyAway:   "E",
}

func KnownHotkeyAction(action string) bool {
	_, ok := defaultHotkeys[action]
	return ok
}

// NormalizeHotkeyKey accepts one Latin letter or digit in any case and
// returns it upper-cased. The key is a physical key, not a typed character.
func NormalizeHotkeyKey(value string) (string, bool) {
	if len(value) != 1 {
		return "", false
	}
	c := value[0]
	if c >= 'a' && c <= 'z' {
		c -= 'a' - 'A'
	}
	if (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
		return string(c), true
	}
	return "", false
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

func NormalizeSoundPack(value string) string {
	switch value {
	case SoundPackShort, SoundPackSoft, SoundPackLow, SoundPackDouble, SoundPackSignal:
		return value
	default:
		return DefaultSoundPack
	}
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
	case ThemeLight, ThemeNeutral, ThemeDark, ThemeBeige, ThemeBrown, ThemePurple:
		return value
	default:
		return ThemeSystem
	}
}

type persistedSettings struct {
	Version int `json:"version"`
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
			persisted.SoundVolume = DefaultSoundVolume
		}
		if _, exists := fields["muteOnAway"]; !exists {
			persisted.MuteOnAway = true
		}
		if _, exists := fields["hotkeyNotifications"]; !exists {
			persisted.HotkeyNotifications = true
		}
	}
	persisted.Theme = NormalizeTheme(persisted.Theme)
	persisted.SoundPack = NormalizeSoundPack(persisted.SoundPack)
	persisted.SoundCues = NormalizeSoundCues(persisted.SoundCues)
	persisted.Hotkeys = NormalizeHotkeys(persisted.Hotkeys)
	if persisted.SoundVolume < 0 || persisted.SoundVolume > 1 {
		persisted.SoundVolume = DefaultSoundVolume
	}
	return persisted.Settings, nil
}

func (store *Store) Save(settings Settings) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	data, err := json.MarshalIndent(persistedSettings{Version: version, Settings: settings}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		return fmt.Errorf("create settings directory: %w", err)
	}
	if err := os.WriteFile(store.path, data, 0o600); err != nil {
		return fmt.Errorf("write settings: %w", err)
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
