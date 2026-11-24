package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"sync"
)

// Setting describes one key of the user's config file (config.json in Dir),
// edited with `remohop config set`. Packages declare the keys they read with
// RegisterSetting so the command can validate and document them.
type Setting struct {
	Key  string
	Help string
	// Validate rejects malformed values; nil accepts anything.
	Validate func(value string) error
	// Secret values are masked when all settings are listed.
	Secret bool
	// Default is shown in listings for a key that is not set.
	Default string
}

var (
	settingsMu sync.RWMutex
	settings   = map[string]Setting{}
)

// RegisterSetting declares a config key. It panics on a duplicate so two
// packages cannot silently share one.
func RegisterSetting(s Setting) {
	settingsMu.Lock()
	defer settingsMu.Unlock()
	if _, dup := settings[s.Key]; dup {
		panic("config: duplicate setting " + s.Key)
	}
	settings[s.Key] = s
}

// Settings lists the declared keys, sorted.
func Settings() []Setting {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	out := make([]Setting, 0, len(settings))
	for _, s := range settings {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// LookupSetting finds a declared key.
func LookupSetting(key string) (Setting, bool) {
	settingsMu.RLock()
	defer settingsMu.RUnlock()
	s, ok := settings[key]
	return s, ok
}

func settingsPath() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

// All returns every value stored in the config file.
func All() (map[string]string, error) {
	p, err := settingsPath()
	if err != nil {
		return nil, err
	}
	return readSettings(p)
}

func readSettings(p string) (map[string]string, error) {
	b, err := ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return m, nil
}

// Get returns the stored value for key, or "" when it is not set.
func Get(key string) (string, error) {
	m, err := All()
	if err != nil {
		return "", err
	}
	return m[key], nil
}

// Set stores value under key; an empty value removes the key. The key must
// have been declared and the value must pass its validation.
func Set(key, value string) error {
	s, ok := LookupSetting(key)
	if !ok {
		return fmt.Errorf("unknown config key %q", key)
	}
	if value != "" && s.Validate != nil {
		if err := s.Validate(value); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	p, err := settingsPath()
	if err != nil {
		return err
	}
	unlock, err := Lock(p)
	if err != nil {
		return err
	}
	defer unlock()
	m, err := readSettings(p)
	if err != nil {
		return err
	}
	if value == "" {
		delete(m, key)
	} else {
		m[key] = value
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// 0600: the file may hold a fixed host password.
	return WriteFileAtomic(p, append(b, '\n'), 0o600)
}

// AllIn returns every value stored in the config file of a specific config
// directory. Long-running processes that were started on a directory read
// their live settings through it.
func AllIn(dir string) (map[string]string, error) {
	return readSettings(filepath.Join(dir, "config.json"))
}

// Bool interprets a stored on/off value; anything else, including an unset
// key, yields def.
func Bool(value string, def bool) bool {
	switch value {
	case "on":
		return true
	case "off":
		return false
	}
	return def
}

// ValidateOnOff is a Setting.Validate for on/off keys.
func ValidateOnOff(v string) error {
	if v != "on" && v != "off" {
		return errors.New("must be on or off")
	}
	return nil
}
