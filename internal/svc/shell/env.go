package shell

import (
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
)

// homeDir is the host user's home directory.
func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		return h
	}
	if u, err := user.Current(); err == nil && u.HomeDir != "" {
		return u.HomeDir
	}
	return string(filepath.Separator)
}

// resolveDir turns a requested working directory into an absolute host
// path: empty means home, "~" and "~/..." are relative to home, other
// relative paths are relative to home too (as they are over ssh).
func resolveDir(dir string) string {
	home := homeDir()
	switch {
	case dir == "" || dir == "~":
		return home
	case strings.HasPrefix(dir, "~/"):
		return filepath.Join(home, dir[2:])
	case filepath.IsAbs(dir):
		return filepath.Clean(dir)
	}
	return filepath.Join(home, dir)
}

// buildEnv is the environment for every process the host starts: the host's
// own environment minus RemoHop's internal variables (REMOHOP_PASSWORD and
// friends must not leak into user shells), with the login basics filled in,
// PWD matching the working directory dir (the host's own would be stale),
// TERM from the client when there is a terminal, then the caller's
// overrides.
func buildEnv(term, dir string, extra []string) []string {
	vars := map[string]string{}
	var order []string
	set := func(k, v string) {
		if runtime.GOOS == "windows" {
			k = strings.ToUpper(k)
		}
		if _, ok := vars[k]; !ok {
			order = append(order, k)
		}
		vars[k] = v
	}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || strings.HasPrefix(k, "REMOHOP_") || k == "TERM" || k == "PWD" || k == "OLDPWD" {
			continue
		}
		set(k, v)
	}
	if runtime.GOOS != "windows" {
		set("HOME", homeDir())
		if u, err := user.Current(); err == nil {
			set("USER", u.Username)
			set("LOGNAME", u.Username)
		}
		set("SHELL", userShell())
		if dir != "" {
			set("PWD", dir)
		}
		if vars["PATH"] == "" {
			set("PATH", "/usr/local/bin:/usr/bin:/bin:/usr/local/sbin:/usr/sbin:/sbin")
		}
	}
	if term != "" {
		set("TERM", term)
	} else {
		for _, kv := range pipedEnv() {
			k, v, _ := strings.Cut(kv, "=")
			set(k, v)
		}
	}
	for _, kv := range extra {
		k, v, _ := strings.Cut(kv, "=")
		set(k, v)
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+vars[k])
	}
	return out
}

// validTerm bounds the TERM value a client may send.
func validTerm(t string) string {
	if len(t) > 64 || strings.ContainsAny(t, "\x00=/ \t\n") {
		return "xterm-256color"
	}
	return t
}
