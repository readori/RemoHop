package x11disp

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
)

// DefaultSocketDir is where local X servers put their sockets.
const DefaultSocketDir = "/tmp/.X11-unix"

// socketDirOverride replaces DefaultSocketDir for RealEnv when set.
var socketDirOverride atomic.Pointer[string]

// SetSocketDir makes RealEnv look for X sockets in dir instead of
// DefaultSocketDir, and returns a function that undoes it. Tests use it to
// hide the machine's real X servers. It is safe to call while discovery is
// running in other goroutines, which a test tearing down a session cannot
// rule out.
func SetSocketDir(dir string) (restore func()) {
	old := socketDirOverride.Swap(&dir)
	return func() { socketDirOverride.Store(old) }
}

func currentSocketDir() string {
	if p := socketDirOverride.Load(); p != nil {
		return *p
	}
	return DefaultSocketDir
}

// Env is everything discovery learns from the machine, behind function
// fields so tests can simulate any host.
type Env struct {
	Getenv   func(string) string
	Glob     func(pattern string) []string
	ReadFile func(name string) ([]byte, error)
	UID      int
	Home     string
	Hostname string
	// SocketDir overrides DefaultSocketDir.
	SocketDir string
}

// RealEnv describes this machine and process.
func RealEnv() *Env {
	home, _ := os.UserHomeDir()
	host, _ := os.Hostname()
	return &Env{
		Getenv: os.Getenv,
		Glob: func(p string) []string {
			m, _ := filepath.Glob(p)
			return m
		},
		ReadFile:  os.ReadFile,
		UID:       os.Getuid(),
		Home:      home,
		Hostname:  host,
		SocketDir: currentSocketDir(),
	}
}

func (e *Env) socketDir() string {
	if e.SocketDir != "" {
		return e.SocketDir
	}
	return DefaultSocketDir
}

// key identifies the inputs a cached result depends on.
func (e *Env) key() string {
	return fmt.Sprintf("%s|%s|%s|%d", e.Getenv("DISPLAY"), e.Getenv("XAUTHORITY"), e.socketDir(), e.UID)
}

// xauthFiles lists the places a cookie for display n may live. A daemon
// started from SSH or systemd does not inherit the desktop session's
// XAUTHORITY, so the usual session locations are searched.
func (e *Env) xauthFiles(n int) []string {
	run := fmt.Sprintf("/run/user/%d", e.UID)
	var files []string
	add := func(f ...string) {
		for _, x := range f {
			if x != "" {
				files = append(files, x)
			}
		}
	}
	add(e.Getenv("XAUTHORITY"))
	if e.Home != "" {
		add(filepath.Join(e.Home, ".Xauthority"))
	}
	add(run + "/gdm/Xauthority")
	add(e.Glob(run + "/.mutter-Xwaylandauth.*")...)
	add(e.Glob(run + "/xauth_*")...)
	add(e.Glob("/var/run/lightdm/root/:" + strconv.Itoa(n))...)
	add(e.Glob("/run/lightdm/root/:" + strconv.Itoa(n))...)
	add(e.Glob("/run/sddm/xauth_*")...)
	add(e.Glob("/var/run/sddm/*")...)
	if e.UID == 0 {
		// Root can read other users' session cookies.
		add(e.Glob("/run/user/*/gdm/Xauthority")...)
		add(e.Glob("/run/user/*/.mutter-Xwaylandauth.*")...)
		add(e.Glob("/run/user/*/xauth_*")...)
		add(e.Glob("/home/*/.Xauthority")...)
	}
	return files
}
