package handoff

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Seams for tests.
var (
	// snapDesktopDir holds the desktop files of installed Snaps.
	snapDesktopDir = "/var/lib/snapd/desktop/applications"
	// flatpakDesktopDirs hold the exported desktop files of Flatpaks
	// (system-wide, then per user under $HOME).
	flatpakDesktopDirs = []string{"/var/lib/flatpak/exports/share/applications", ".local/share/flatpak/exports/share/applications"}
	// snapBin is where snapd puts the commands of installed Snaps.
	snapBin = "/snap/bin"
	// defaultBrowserID asks the desktop for the default browser's desktop
	// file name, e.g. "firefox_firefox.desktop".
	defaultBrowserID = queryDefaultBrowser
	homeDir          = os.UserHomeDir
)

// sandboxDir is a directory inside the sandbox of browser if it is a Snap
// or a Flatpak, or "" if it is not (or that cannot be told).
func sandboxDir(browser string) string {
	home, err := homeDir()
	if err != nil || home == "" {
		return ""
	}
	if browser != "" {
		if name := snapOfProgram(browser); name != "" {
			return snapDir(home, name)
		}
		return ""
	}
	id := defaultBrowserID()
	if id == "" {
		return ""
	}
	if name := snapOfDesktopID(id); name != "" {
		return snapDir(home, name)
	}
	if app := flatpakOfDesktopID(home, id); app != "" {
		// ~/.var/app/<id> is the Flatpak's own data directory, always
		// readable and writable by it.
		base := filepath.Join(home, ".var", "app", app)
		if isDir(base) {
			return filepath.Join(base, "cache", "remohop")
		}
	}
	return ""
}

// snapDir is the handoff directory for the Snap called name: its
// per-revision-independent "common" directory, which the Snap can always
// read, unlike hidden directories elsewhere in the home directory.
func snapDir(home, name string) string {
	base := filepath.Join(home, "snap", name)
	if !isDir(base) {
		// The Snap has never run for this user; snapd creates the directory
		// on first run, and an existing one is what proves it is ours.
		if err := os.MkdirAll(filepath.Join(base, "common"), 0o755); err != nil {
			return ""
		}
	}
	return filepath.Join(base, "common", "remohop")
}

// snapOfDesktopID is the Snap name for a desktop file name such as
// "firefox_firefox.desktop" if a Snap installed it.
func snapOfDesktopID(id string) string {
	if !strings.HasSuffix(id, ".desktop") || strings.ContainsAny(id, "/\\") {
		return ""
	}
	if !isFile(filepath.Join(snapDesktopDir, id)) {
		return ""
	}
	name, _, ok := strings.Cut(strings.TrimSuffix(id, ".desktop"), "_")
	if !ok || name == "" {
		return ""
	}
	return name
}

// flatpakOfDesktopID is the Flatpak application ID for a desktop file name
// such as "org.mozilla.firefox.desktop" if a Flatpak exported it.
func flatpakOfDesktopID(home, id string) string {
	if !strings.HasSuffix(id, ".desktop") || strings.ContainsAny(id, "/\\") {
		return ""
	}
	for _, d := range flatpakDesktopDirs {
		if !filepath.IsAbs(d) {
			d = filepath.Join(home, d)
		}
		if isFile(filepath.Join(d, id)) {
			return strings.TrimSuffix(id, ".desktop")
		}
	}
	return ""
}

// snapOfProgram is the Snap name of a browser program given by path or
// name (--app-browser): /snap/bin/chromium, or a wrapper script such as
// Ubuntu's /usr/bin/chromium-browser that runs one.
func snapOfProgram(browser string) string {
	p := browser
	if !strings.ContainsRune(p, '/') {
		if lp, err := exec.LookPath(p); err == nil {
			p = lp
		}
	}
	if filepath.Dir(p) == snapBin {
		return filepath.Base(p)
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		// A link to /snap/bin/NAME (whose own target, /usr/bin/snap, says
		// nothing about which Snap it is).
		if l, err := os.Readlink(p); err == nil && filepath.Dir(l) == snapBin {
			return filepath.Base(l)
		}
		p = r
	}
	// A small shell script that execs /snap/bin/NAME.
	if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() && fi.Size() < 16<<10 {
		if b, err := os.ReadFile(p); err == nil && bytes.HasPrefix(b, []byte("#!")) {
			if i := bytes.Index(b, []byte(snapBin+"/")); i >= 0 {
				rest := b[i+len(snapBin)+1:]
				end := bytes.IndexFunc(rest, func(r rune) bool {
					return !(r == '-' || r == '_' || r == '.' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
				})
				if end < 0 {
					end = len(rest)
				}
				if name := string(rest[:end]); name != "" {
					return name
				}
			}
		}
	}
	return ""
}

// queryDefaultBrowser asks xdg-settings, then xdg-mime, for the desktop
// file name of the default web browser. Empty when neither answers.
func queryDefaultBrowser() string {
	for _, args := range [][]string{
		{"xdg-settings", "get", "default-web-browser"},
		{"xdg-mime", "query", "default", "x-scheme-handler/https"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).Output()
		cancel()
		if id := strings.TrimSpace(string(out)); err == nil && strings.HasSuffix(id, ".desktop") {
			return id
		}
	}
	return ""
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
