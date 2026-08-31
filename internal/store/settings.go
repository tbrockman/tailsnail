package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// settingsVersion is the on-disk schema version of settings.json.
const settingsVersion = 1

// Settings holds the user preferences that survive between runs. Command-line
// flags override these for a single run without rewriting the file.
type Settings struct {
	Version     int        `json:"version"`
	DisplayName string     `json:"display_name,omitempty"`
	Theme       string     `json:"theme,omitempty"`
	ASCII       bool       `json:"ascii,omitempty"`
	ColorMode   string     `json:"color_mode,omitempty"`
	ShowNodeID  bool       `json:"show_node_id,omitempty"`
	LastConfig  *HostPrefs `json:"last_host_config,omitempty"`
	// Emoji allows the snail icon where the terminal is known to support it.
	// Detection still has the final say; this only ever turns it off.
	Emoji bool `json:"emoji"`
	// AutoResize lets the app ask the terminal to grow to fit an arena.
	AutoResize bool `json:"auto_resize"`
}

// HostPrefs remembers the last lobby a user hosted so the form comes back
// pre-filled rather than resetting to defaults every time.
type HostPrefs struct {
	Name         string `json:"name"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	TickRate     int    `json:"tick_rate"`
	TicksPerMove int    `json:"ticks_per_move"`
	MaxPlayers   int    `json:"max_players"`
	Bots         int    `json:"bots,omitempty"`
	Wrap         bool   `json:"wrap"`
	Mode         string `json:"mode"`
}

// DefaultSettings returns the settings a fresh install starts with.
func DefaultSettings() Settings {
	return Settings{
		Version:    settingsVersion,
		Theme:      "neon",
		ColorMode:  "auto",
		ShowNodeID: true,
		Emoji:      true,
		AutoResize: true,
	}
}

// settingsPath returns the location of the settings file.
func settingsPath(stateDir string) string { return filepath.Join(stateDir, "settings.json") }

// LoadSettings reads settings.json, returning defaults when it is absent.
// A corrupt file is reported so the user can be told rather than silently
// losing their preferences.
func LoadSettings(stateDir string) (Settings, error) {
	raw, err := os.ReadFile(settingsPath(stateDir))
	if errors.Is(err, os.ErrNotExist) {
		return DefaultSettings(), nil
	}
	if err != nil {
		return DefaultSettings(), fmt.Errorf("store: reading settings: %w", err)
	}
	s := DefaultSettings()
	if err := json.Unmarshal(raw, &s); err != nil {
		return DefaultSettings(), fmt.Errorf("store: parsing settings: %w", err)
	}
	s.Version = settingsVersion
	return s, nil
}

// SaveSettings writes settings.json atomically.
func SaveSettings(stateDir string, s Settings) error {
	s.Version = settingsVersion
	raw, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("store: encoding settings: %w", err)
	}
	return writeFileAtomic(settingsPath(stateDir), append(raw, '\n'))
}
