// Package update finds out whether a newer RemoHop release exists and how
// this copy should be upgraded. It only reads: it downloads nothing but a
// small version file and never replaces a program.
//
// The version file is https://remohop.com/version.json, a static file of
// the website ({"version": "v1.2.3"}), built from site/build.py. The request
// carries no identifier: no machine ID, no account, and the User-Agent is
// just "RemoHop". Everything the app shows (the download page, the release
// notes) is an address this package builds from the version number, so a
// tampered file can at most claim a version, never send the user elsewhere.
package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// URL is where the version file lives.
const URL = "https://remohop.com/version.json"

// Repo is the GitHub repository the release pages are in.
const Repo = "readori/RemoHop"

// maxBody bounds the version file.
const maxBody = 16 << 10

// Release is what the version file says.
type Release struct {
	Version string `json:"version"`
}

// NotesURL is the release's page on GitHub (its notes and files).
func (r Release) NotesURL() string {
	return "https://github.com/" + Repo + "/releases/tag/" + r.Version
}

// DownloadURL is the website's download page in the given language.
func DownloadURL(lang string) string {
	if lang == "zh" {
		return "https://remohop.com/download/"
	}
	return "https://remohop.com/en/download/"
}

// Fetch reads the version file at url (URL when empty).
func Fetch(ctx context.Context, client *http.Client, url string) (Release, error) {
	if url == "" {
		url = URL
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("User-Agent", "RemoHop")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("version file: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return Release{}, err
	}
	if len(b) > maxBody {
		return Release{}, errors.New("version file: too large")
	}
	return Parse(b)
}

// Parse reads a version file. Only a plain release version (vX.Y.Z) is
// accepted.
func Parse(b []byte) (Release, error) {
	var r Release
	if err := json.Unmarshal(b, &r); err != nil {
		return Release{}, fmt.Errorf("version file: %w", err)
	}
	v, ok := ParseVersion(r.Version)
	if !ok || v.Pre != "" {
		return Release{}, fmt.Errorf("version file: %q is not a release version", r.Version)
	}
	return r, nil
}

// Version is a parsed vMAJOR.MINOR.PATCH[-PRE].
type Version struct {
	Major, Minor, Patch int
	Pre                 string
}

var versionRE = regexp.MustCompile(`^v(0|[1-9][0-9]{0,5})\.(0|[1-9][0-9]{0,5})\.(0|[1-9][0-9]{0,5})(?:-([0-9A-Za-z.-]{1,40}))?$`)

// ParseVersion parses "v1.2.3" or "v1.2.3-rc.1". Development builds
// ("dev", or anything else) do not parse.
func ParseVersion(s string) (Version, bool) {
	m := versionRE.FindStringSubmatch(s)
	if m == nil {
		return Version{}, false
	}
	n := func(x string) int { i, _ := strconv.Atoi(x); return i }
	return Version{Major: n(m[1]), Minor: n(m[2]), Patch: n(m[3]), Pre: m[4]}, true
}

// Less reports whether v comes before w. A pre-release comes before its
// release; two pre-releases of the same version compare as text, which
// is enough to tell any of them from the release.
func (v Version) Less(w Version) bool {
	if v.Major != w.Major {
		return v.Major < w.Major
	}
	if v.Minor != w.Minor {
		return v.Minor < w.Minor
	}
	if v.Patch != w.Patch {
		return v.Patch < w.Patch
	}
	switch {
	case v.Pre == w.Pre:
		return false
	case v.Pre == "":
		return false
	case w.Pre == "":
		return true
	}
	return v.Pre < w.Pre
}

// Newer reports whether latest is a newer version than current. A current
// version that does not parse (a development build) is never out of date.
func Newer(latest, current string) bool {
	l, ok1 := ParseVersion(latest)
	c, ok2 := ParseVersion(current)
	return ok1 && ok2 && c.Less(l)
}

// Kinds of installation.
const (
	KindDownload = "download" // a download from the website: get the new one there
	KindHomebrew = "homebrew"
	KindScoop    = "scoop"
	KindWinget   = "winget"
	KindSnap     = "snap"
	KindPackage  = "package" // .deb, .rpm, .apk, AUR: the system's package manager
	KindNix      = "nix"
)

// Method is how this copy is upgraded.
type Method struct {
	Kind string `json:"kind"`
	// Command upgrades it, when there is one command for it.
	Command string `json:"command,omitempty"`
}

// Env is what Detect looks at; tests replace it.
type Env struct {
	GOOS   string
	Exe    string // the program's path, symbolic links resolved
	Getenv func(string) string
	Exists func(string) bool
}

// Detect works out how the program at env.Exe was installed, from where it
// is. A copy it does not recognise is a download.
func Detect(env Env) Method {
	if env.Getenv == nil {
		env.Getenv = func(string) string { return "" }
	}
	if env.Exists == nil {
		env.Exists = func(string) bool { return false }
	}
	if env.GOOS == "" {
		env.GOOS = runtime.GOOS
	}
	exe := strings.ReplaceAll(env.Exe, `\`, "/")
	low := strings.ToLower(exe)
	switch {
	case env.Getenv("SNAP") != "" || strings.HasPrefix(exe, "/snap/"):
		return Method{Kind: KindSnap, Command: "sudo snap refresh remohop"}
	case strings.HasPrefix(exe, "/nix/store/"):
		return Method{Kind: KindNix}
	}
	switch env.GOOS {
	case "windows":
		switch {
		case strings.Contains(low, "/scoop/apps/remohop/"):
			return Method{Kind: KindScoop, Command: "scoop update remohop"}
		case strings.Contains(low, "/microsoft/winget/packages/"):
			return Method{Kind: KindWinget, Command: "winget upgrade RemoHop.RemoHop"}
		}
	case "darwin", "linux":
		if strings.Contains(exe, "/Cellar/remohop/") {
			return Method{Kind: KindHomebrew, Command: "brew upgrade remohop"}
		}
		// The cask puts RemoHop.app in /Applications, where a copy dragged
		// out of the disk image goes too; the Caskroom tells them apart.
		if env.GOOS == "darwin" && strings.Contains(exe, "/RemoHop.app/Contents/MacOS/") {
			for _, room := range []string{"/opt/homebrew/Caskroom/remohop-quick", "/usr/local/Caskroom/remohop-quick"} {
				if env.Exists(room) {
					return Method{Kind: KindHomebrew, Command: "brew upgrade --cask remohop-quick"}
				}
			}
		}
		if env.GOOS == "linux" && (exe == "/usr/bin/remohop" || exe == "/usr/sbin/remohop") {
			return Method{Kind: KindPackage}
		}
	}
	return Method{Kind: KindDownload}
}
