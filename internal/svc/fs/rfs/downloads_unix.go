//go:build unix

package rfs

import (
	"bufio"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// downloadsDir is the host user's download directory: on Linux and other
// XDG systems XDG_DOWNLOAD_DIR from user-dirs.dirs (what `xdg-user-dir
// DOWNLOAD` reads, without running it), else ~/Downloads.
func downloadsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	fallback := filepath.Join(home, "Downloads")
	if runtime.GOOS == "darwin" {
		return fallback, nil
	}
	cfg := os.Getenv("XDG_CONFIG_HOME")
	if cfg == "" || !filepath.IsAbs(cfg) {
		cfg = filepath.Join(home, ".config")
	}
	if d := xdgUserDir(filepath.Join(cfg, "user-dirs.dirs"), "XDG_DOWNLOAD_DIR", home); d != "" {
		return d, nil
	}
	return fallback, nil
}

// xdgUserDir parses one entry of a user-dirs.dirs file: lines of the form
// XDG_xxx_DIR="$HOME/yyy" or XDG_xxx_DIR="/abs/path". As in xdg-user-dir, a
// value equal to $HOME itself means "not set".
func xdgUserDir(file, key, home string) string {
	f, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != key {
			continue
		}
		v = strings.TrimSpace(v)
		if len(v) < 2 || v[0] != '"' || v[len(v)-1] != '"' {
			continue
		}
		v = v[1 : len(v)-1]
		switch {
		case v == "$HOME" || v == "$HOME/":
			return ""
		case strings.HasPrefix(v, "$HOME/"):
			v = filepath.Join(home, v[len("$HOME/"):])
		case !filepath.IsAbs(v):
			continue
		}
		return filepath.Clean(v)
	}
	return ""
}
