package clip

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/x11disp"
)

var (
	virtualOnce sync.Once
	virtual     *MemoryBoard
)

// Virtual returns the process-wide in-memory clipboard, shared by every
// session of a headless host.
func Virtual() *MemoryBoard {
	virtualOnce.Do(func() { virtual = NewMemoryBoard() })
	return virtual
}

// Detect picks the clipboard backend for this machine: a working system
// clipboard if one can be reached, otherwise the process's virtual one. It
// never returns nil. Detection probes like a user's shell would, so a daemon
// started without DISPLAY/WAYLAND_DISPLAY still finds the desktop session.
func Detect(ctx context.Context) Board {
	if b := detectSystem(ctx); b != nil {
		return b
	}
	return Virtual()
}

func detectSystem(ctx context.Context) Board {
	switch runtime.GOOS {
	case "darwin":
		if have("pbcopy") && have("pbpaste") {
			return &macBoard{}
		}
	case "windows":
		for _, exe := range []string{"powershell.exe", "pwsh.exe"} {
			if have(exe) {
				return &winBoard{exe: exe}
			}
		}
	default:
		return detectUnix(ctx)
	}
	return nil
}

func have(tool string) bool {
	_, err := exec.LookPath(tool)
	return err == nil
}

// usable reports whether b answers: an empty clipboard is fine, an
// unreachable display is not.
func usable(ctx context.Context, b Board) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, err := b.Read(ctx)
	return err == nil || errors.Is(err, ErrEmpty) || errors.Is(err, ErrTooLarge)
}

func detectUnix(ctx context.Context) Board {
	if have("wl-paste") && have("wl-copy") {
		for _, env := range waylandEnvs() {
			if b := (&waylandBoard{h: helper{env: env}}); usable(ctx, b) {
				return b
			}
		}
	}
	var tools []string
	for _, t := range []string{"xclip", "xsel"} {
		if have(t) {
			tools = append(tools, t)
		}
	}
	if len(tools) == 0 {
		return nil
	}
	for _, env := range x11Envs(ctx) {
		for _, t := range tools {
			if b := (&x11Board{tool: t, h: helper{env: env}}); usable(ctx, b) {
				return b
			}
		}
	}
	return nil
}

// withEnv returns the process environment with the given overrides.
func withEnv(overrides map[string]string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := overrides[k]; !ok {
			env = append(env, kv)
		}
	}
	for k, v := range overrides {
		env = append(env, k+"="+v)
	}
	return env
}

// waylandEnvs lists candidate environments: the inherited one first, then
// every compositor socket found in the runtime directory.
func waylandEnvs() [][]string {
	var out [][]string
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		out = append(out, nil)
	}
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = fmt.Sprintf("/run/user/%d", os.Getuid())
	}
	socks, _ := filepath.Glob(filepath.Join(dir, "wayland-*"))
	sort.Strings(socks)
	for _, s := range socks {
		if strings.HasSuffix(s, ".lock") {
			continue
		}
		out = append(out, withEnv(map[string]string{
			"WAYLAND_DISPLAY": filepath.Base(s),
			"XDG_RUNTIME_DIR": dir,
		}))
	}
	return out
}

// x11Envs returns the environment for the X display a clipboard tool should
// use. Discovery is shared with the other services (internal/x11disp): it
// skips stale sockets and finds the cookie a daemon without XAUTHORITY needs.
func x11Envs(ctx context.Context) [][]string {
	d, err := x11disp.Find(ctx)
	if err != nil {
		return nil
	}
	return [][]string{d.Merge(nil)}
}

// hostBoards holds the host daemon's clipboard. A host that started before
// the desktop session existed falls back to the virtual clipboard; it looks
// again (at most every redetectEvery) so a later login is picked up.
var hostBoards struct {
	mu       sync.Mutex
	board    Board
	override bool
	at       time.Time
}

const redetectEvery = 30 * time.Second

// SetHostBoard fixes the clipboard the host side serves, bypassing detection.
// It exists for tests and for embedding; nil restores detection.
func SetHostBoard(b Board) {
	hostBoards.mu.Lock()
	defer hostBoards.mu.Unlock()
	hostBoards.board, hostBoards.override, hostBoards.at = b, b != nil, time.Time{}
}

func hostBoard(ctx context.Context) Board {
	hostBoards.mu.Lock()
	defer hostBoards.mu.Unlock()
	if hostBoards.override {
		return hostBoards.board
	}
	_, isVirtual := hostBoards.board.(*MemoryBoard)
	if hostBoards.board == nil || (isVirtual && time.Since(hostBoards.at) > redetectEvery) {
		hostBoards.board, hostBoards.at = Detect(ctx), time.Now()
	}
	return hostBoards.board
}
