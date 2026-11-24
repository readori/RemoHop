package clip

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/readori/RemoHop/internal/config"
)

// settings is the persisted clipboard configuration of this machine
// (clip/settings.json under the config directory). The fields are negative
// so that a missing file means "everything on".
type settings struct {
	// RecordOff stops the client-side history from being written.
	RecordOff bool `json:"record_off,omitempty"`
	// HostRecordOff stops the host daemon from recording what passes
	// through its clipboard during sessions.
	HostRecordOff bool `json:"host_record_off,omitempty"`
}

func settingsPath() (string, error) {
	dir, err := config.SubDir("clip")
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

// loadSettings reads the settings; anything unreadable counts as defaults.
func loadSettings() settings {
	var s settings
	if p, err := settingsPath(); err == nil {
		if b, err := os.ReadFile(p); err == nil {
			json.Unmarshal(b, &s)
		}
	}
	return s
}

func updateSettings(f func(*settings)) error {
	p, err := settingsPath()
	if err != nil {
		return err
	}
	s := loadSettings()
	f(&s)
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// RecordingEnabled reports whether clipboard items are recorded in the
// local (client-side) history.
func RecordingEnabled() bool { return !loadSettings().RecordOff }

// SetRecording turns the client-side history recording on or off. The
// choice is persisted; while off, nothing is written.
func SetRecording(on bool) error {
	return updateSettings(func(s *settings) { s.RecordOff = !on })
}

// HostRecordingEnabled reports whether this machine, acting as a host,
// records what passes through its clipboard during sessions.
func HostRecordingEnabled() bool { return !loadSettings().HostRecordOff }

// SetHostRecording turns the host-side history recording on or off
// (persisted).
func SetHostRecording(on bool) error {
	return updateSettings(func(s *settings) { s.HostRecordOff = !on })
}

// OpenHostHistory opens the history the host daemon keeps of its own
// clipboard traffic: clip/host under the config directory, with the same
// caps and permissions as the client-side one.
func OpenHostHistory() (*History, error) {
	dir, err := config.SubDir("clip")
	if err != nil {
		return nil, err
	}
	h, err := OpenHistory(filepath.Join(dir, "host"))
	if err != nil {
		return nil, err
	}
	h.hostSide = true
	return h, nil
}
