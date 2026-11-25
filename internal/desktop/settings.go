package desktop

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/config"
)

// settingsFile holds the app's own settings in the config directory. The
// rendezvous server is not among them: it is the `signal` setting that the
// command line uses too, so the app and `remohop` agree on it.
const settingsFile = "desktop.json"

// OfficialServer is the public rendezvous server the first-run wizard
// offers. Nothing uses it unless the user picks it.
const OfficialServer = "signal.remohop.com"

// Limits on what the settings file keeps.
const (
	maxTrusted = 100
	maxSeen    = 300
)

// Settings are the app's choices, kept across runs.
type Settings struct {
	// Setup is true once the first-run wizard is done (or skipped).
	Setup bool `json:"setup"`
	// Lang is "zh", "en" or "" (follow the system).
	Lang string `json:"lang,omitempty"`
	// Theme is "light", "dark" or "" (follow the system).
	Theme string `json:"theme,omitempty"`
	// Incoming: others may connect to this computer while the app runs
	// (every connection still asks). Off until the user turns it on.
	Incoming bool `json:"incoming"`
	// Trusted are the devices the user chose to remember: their next
	// logins are let in with the access chosen then, without a question,
	// while incoming connections are on.
	Trusted []TrustedDevice `json:"trusted,omitempty"`
	// ServiceID is the Windows unattended-access service's ID, as the
	// elevated steps last found it (its state is administrators' only).
	ServiceID string `json:"service_id,omitempty"`
	// Seen are the devices that asked before, so a request can say
	// whether this one is new. Oldest first; bounded.
	Seen []SeenDevice `json:"seen,omitempty"`
}

// TrustedDevice is a remembered device.
type TrustedDevice struct {
	Identity string    `json:"identity"`
	Name     string    `json:"name,omitempty"`
	Control  bool      `json:"control"`
	Files    bool      `json:"files"`
	Added    time.Time `json:"added"`
}

// SeenDevice is a device that asked to connect before.
type SeenDevice struct {
	Identity string    `json:"identity"`
	First    time.Time `json:"first"`
	Last     time.Time `json:"last"`
}

func loadSettings(dir string) (Settings, error) {
	var s Settings
	b, err := config.ReadFile(filepath.Join(dir, settingsFile))
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return Settings{}, err
	}
	s.Lang = normLang(s.Lang)
	s.Theme = normTheme(s.Theme)
	return s, nil
}

func saveSettings(dir string, s Settings) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(filepath.Join(dir, settingsFile), append(b, '\n'), 0o600)
}

func normLang(l string) string {
	l = strings.ToLower(strings.TrimSpace(l))
	switch {
	case strings.HasPrefix(l, "zh"):
		return "zh"
	case strings.HasPrefix(l, "en"):
		return "en"
	}
	return ""
}

func normTheme(t string) string {
	switch t {
	case "light", "dark":
		return t
	}
	return ""
}

func (s *Settings) trusted(identity string) (TrustedDevice, bool) {
	for _, t := range s.Trusted {
		if t.Identity == identity {
			return t, true
		}
	}
	return TrustedDevice{}, false
}

func (s *Settings) trust(t TrustedDevice) {
	s.forget(t.Identity)
	s.Trusted = append(s.Trusted, t)
	if len(s.Trusted) > maxTrusted {
		s.Trusted = s.Trusted[len(s.Trusted)-maxTrusted:]
	}
}

func (s *Settings) forget(identity string) bool {
	for i, t := range s.Trusted {
		if t.Identity == identity {
			s.Trusted = append(s.Trusted[:i:i], s.Trusted[i+1:]...)
			return true
		}
	}
	return false
}

// see records that identity asked now and reports whether it had before.
func (s *Settings) see(identity string, now time.Time) (before bool) {
	for i, d := range s.Seen {
		if d.Identity == identity {
			s.Seen[i].Last = now
			return true
		}
	}
	s.Seen = append(s.Seen, SeenDevice{Identity: identity, First: now, Last: now})
	if len(s.Seen) > maxSeen {
		// Keep the most recently seen.
		sort.SliceStable(s.Seen, func(i, j int) bool { return s.Seen[i].Last.Before(s.Seen[j].Last) })
		s.Seen = s.Seen[len(s.Seen)-maxSeen:]
	}
	return false
}
