//go:build linux

package screenshot

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/png" // decoder for the PNG files the Wayland tools write
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/readori/RemoHop/internal/x11disp"
)

// sysEnv is everything the Linux backend learns from the machine, behind
// function fields so tests can simulate a headless server.
type sysEnv struct {
	Getenv   func(string) string
	Glob     func(pattern string) []string
	ReadFile func(name string) ([]byte, error)
	LookPath func(name string) (string, error)
	// Run executes a capture tool with extra environment variables.
	Run      func(ctx context.Context, env []string, name string, args ...string) error
	UID      int
	Home     string
	Hostname string
}

func realSysEnv() *sysEnv {
	home, _ := os.UserHomeDir()
	host, _ := os.Hostname()
	return &sysEnv{
		Getenv: os.Getenv,
		Glob: func(p string) []string {
			m, _ := filepath.Glob(p)
			return m
		},
		ReadFile: os.ReadFile,
		LookPath: exec.LookPath,
		Run: func(ctx context.Context, env []string, name string, args ...string) error {
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Env = append(os.Environ(), env...)
			var out bytes.Buffer
			cmd.Stdout, cmd.Stderr = io.Discard, &out
			if err := cmd.Run(); err != nil {
				if msg := strings.TrimSpace(out.String()); msg != "" {
					return fmt.Errorf("%v: %s", err, lastLine(msg))
				}
				return err
			}
			return nil
		},
		UID:      os.Getuid(),
		Home:     home,
		Hostname: host,
	}
}

func lastLine(s string) string {
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func platformGrabber() grabber {
	return &linuxGrabber{env: realSysEnv(), finder: x11disp.Default()}
}

// x11Env adapts the probing seam for x11disp.
func (e *sysEnv) x11Env() *x11disp.Env {
	return &x11disp.Env{Getenv: e.Getenv, Glob: e.Glob, ReadFile: e.ReadFile, UID: e.UID, Home: e.Home, Hostname: e.Hostname}
}

type linuxGrabber struct {
	env *sysEnv
	// finder caches X display discovery across captures; nil builds a
	// throwaway one over env (tests).
	finder *x11disp.Finder
}

// openX11 connects to the first reachable X display, returning notes on
// what failed otherwise.
func (g *linuxGrabber) openX11(ctx context.Context) (*x11, []string) {
	f := g.finder
	if f == nil {
		f = x11disp.NewFinder(g.env.x11Env())
	}
	d, err := f.Find(ctx)
	if err != nil {
		var fe *x11disp.Error
		if errors.As(err, &fe) {
			return nil, fe.Notes
		}
		return nil, []string{err.Error()}
	}
	c, err := d.Open(ctx)
	if err != nil {
		return nil, []string{err.Error()}
	}
	x, err := newX11(c, d.Screen)
	if err != nil {
		return nil, []string{fmt.Sprintf("X display %s: %v", d.Name, err)}
	}
	return x, nil
}

func (g *linuxGrabber) Displays(ctx context.Context) ([]Display, error) {
	return runBackend(ctx, g, func(x *x11) ([]Display, error) { return x.monitors(), nil },
		func(img image.Image) []Display {
			b := img.Bounds()
			return []Display{{Name: "wayland", Width: b.Dx(), Height: b.Dy(), Primary: true}}
		})
}

func (g *linuxGrabber) Grab(ctx context.Context, display int) (image.Image, error) {
	return runBackend(ctx, g, func(x *x11) (image.Image, error) {
		rect := image.Rect(0, 0, x.width, x.height)
		if display != AllDisplays {
			ds := x.monitors()
			if display >= len(ds) {
				return nil, fmt.Errorf("screenshot: no display %d (the host has %d)", display, len(ds))
			}
			d := ds[display]
			rect = image.Rect(d.X, d.Y, d.X+d.Width, d.Y+d.Height)
		}
		return x.grab(ctx, rect)
	}, func(img image.Image) image.Image { return img })
}

// run tries the backends in the order that fits the session type. X11 is
// native and exact, but on a Wayland session it only shows XWayland clients
// (often a black screen), so there the compositor tools go first. fromX runs
// on a connected X display, fromTool post-processes a tool's image.
func runBackend[T any](ctx context.Context, g *linuxGrabber, fromX func(*x11) (T, error), fromTool func(image.Image) T) (T, error) {
	var zero T
	e := g.env
	wayland := e.Getenv("WAYLAND_DISPLAY") != "" || e.Getenv("XDG_SESSION_TYPE") == "wayland" || len(e.waylandSockets()) > 0
	var notes []string

	tryX := func() (T, bool, error) {
		x, n := g.openX11(ctx)
		notes = append(notes, n...)
		if x == nil {
			return zero, false, nil
		}
		defer x.close()
		v, err := fromX(x)
		return v, true, err
	}
	tryTools := func() (T, bool) {
		img, n := g.waylandCapture(ctx)
		notes = append(notes, n...)
		if img == nil {
			return zero, false
		}
		return fromTool(img), true
	}

	if wayland {
		if v, ok := tryTools(); ok {
			return v, nil
		}
		if v, ok, err := tryX(); ok {
			return v, err
		}
	} else {
		if v, ok, err := tryX(); ok {
			return v, err
		}
		if v, ok := tryTools(); ok {
			return v, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	return zero, errors.New("no display available on the host: " + strings.Join(notes, "; "))
}

func (e *sysEnv) runtimeDirs() []string {
	dirs := []string{fmt.Sprintf("/run/user/%d", e.UID)}
	if d := e.Getenv("XDG_RUNTIME_DIR"); d != "" && d != dirs[0] {
		dirs = append([]string{d}, dirs...)
	}
	if e.UID == 0 {
		dirs = append(dirs, e.Glob("/run/user/*")...)
	}
	return dirs
}

// waylandSockets finds compositor sockets (wayland-0, ...) in the runtime
// directories.
func (e *sysEnv) waylandSockets() []string {
	var out []string
	for _, d := range e.runtimeDirs() {
		for _, s := range e.Glob(d + "/wayland-*") {
			if !strings.HasSuffix(s, ".lock") {
				out = append(out, s)
			}
		}
	}
	return out
}

// toolEnv builds the environment a compositor tool needs when the daemon was
// not started from the desktop session.
func (e *sysEnv) toolEnv() []string {
	var env []string
	socks := e.waylandSockets()
	if e.Getenv("WAYLAND_DISPLAY") == "" && len(socks) > 0 {
		env = append(env, "WAYLAND_DISPLAY="+filepath.Base(socks[0]))
	}
	dir := ""
	if len(socks) > 0 {
		dir = filepath.Dir(socks[0])
	}
	if e.Getenv("XDG_RUNTIME_DIR") == "" && dir != "" {
		env = append(env, "XDG_RUNTIME_DIR="+dir)
	}
	if e.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" && dir != "" && len(e.Glob(dir+"/bus")) > 0 {
		env = append(env, "DBUS_SESSION_BUS_ADDRESS=unix:path="+dir+"/bus")
	}
	return env
}

var waylandTools = []struct {
	name string
	args func(file string) []string
}{
	{"grim", func(f string) []string { return []string{f} }},
	{"gnome-screenshot", func(f string) []string { return []string{"-f", f} }},
	{"spectacle", func(f string) []string { return []string{"-b", "-n", "-o", f} }},
}

// waylandCapture runs the first working compositor screenshot tool and
// decodes its PNG. On failure it returns nil and a note per tool.
func (g *linuxGrabber) waylandCapture(ctx context.Context) (image.Image, []string) {
	var notes []string
	var missing []string
	for _, t := range waylandTools {
		if _, err := g.env.LookPath(t.name); err != nil {
			missing = append(missing, t.name)
			continue
		}
		img, err := g.runTool(ctx, t.name, t.args)
		if err == nil {
			return img, nil
		}
		notes = append(notes, fmt.Sprintf("%s: %v", t.name, err))
	}
	if len(missing) == len(waylandTools) {
		notes = append(notes, "no screenshot tool installed ("+strings.Join(missing, ", ")+")")
	}
	return nil, notes
}

func (g *linuxGrabber) runTool(ctx context.Context, name string, args func(string) []string) (image.Image, error) {
	dir, err := os.MkdirTemp("", "remohop-shot-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	file := filepath.Join(dir, "shot.png")
	if err := g.env.Run(ctx, g.env.toolEnv(), name, args(file)...); err != nil {
		return nil, err
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, fmt.Errorf("tool wrote no file: %w", err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}
