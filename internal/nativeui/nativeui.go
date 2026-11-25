// Package nativeui is the interface of the desktop app's native windows:
// the types internal/desktop programs against, and the registration
// through which internal/webview provides them. Only the desktop app's
// binary (cmd/remohop-quick) links internal/webview; in any other binary
// (the static remohop command line tool) nothing is registered, Probe
// reports ErrUnavailable and the app falls back to a browser window.
package nativeui

import (
	"errors"
	"fmt"
)

// ErrUnavailable means this system has no usable native web view: no
// WebView2 runtime on Windows, no WebKitGTK library on Linux, an unknown
// platform. Callers fall back to a browser window.
var ErrUnavailable = errors.New("webview: no native web view on this system")

// Config configures Run.
type Config struct {
	// AppID names the program to the windowing system: the Linux
	// application name that .desktop files match (WM_CLASS) and the
	// AppIndicator ID.
	AppID string
	// DataDir is where the web engine keeps its profile (cache, local
	// storage). WebView2 needs one it can write; the others ignore it.
	DataDir string
	// IconPNG is the tray icon on Linux and macOS (macOS draws it as a
	// template image: only its alpha channel counts).
	IconPNG []byte
	// TemplatePNG, if set, is the macOS tray icon instead of IconPNG.
	TemplatePNG []byte
	// OnReopen runs when the user asks for the program again while it
	// runs without a visible window (macOS: a click on its Dock icon).
	OnReopen func()
	// OnQuitRequest, if set, runs when the system asks the program to
	// quit (macOS: Quit in the menu or the Dock) instead of quitting; the
	// program calls App.Quit when it is ready.
	OnQuitRequest func()
}

// App is the running native application.
type App interface {
	// NewWindow opens a window showing o.URL.
	NewWindow(o Options) (Window, error)
	// Dispatch runs fn on the UI thread. It may be called from any
	// goroutine, and returns at once.
	Dispatch(fn func())
	// SetTray shows (or with nil removes) the tray icon. It fails where
	// the system has no tray (a Linux desktop without AppIndicator
	// support): callers must then not hide their last window.
	SetTray(t *Tray) error
	// Quit ends Run.
	Quit()
}

// Options configure a window.
type Options struct {
	Title  string
	URL    string
	Width  int // logical pixels; 0 means 960
	Height int // 0 means 680
	// MinWidth and MinHeight bound resizing (0: no bound).
	MinWidth, MinHeight int
	// OnClose runs when the user asks to close the window. Returning false
	// keeps it open (the caller may hide it instead); true closes it.
	// Nil closes it.
	OnClose func() bool
	// Hidden creates the window without showing it.
	Hidden bool
	// Overlay makes a small undecorated window that stays above the
	// others and out of the task bar, in the bottom-right corner of the
	// work area (where notifications appear, clear of the app's own
	// window and tab bar): the "this computer is being controlled" banner.
	Overlay bool
}

// Window is a native window with a web view.
type Window interface {
	Navigate(url string)
	SetTitle(title string)
	// Show shows the window and brings it to the front.
	Show()
	Hide()
	// Close closes the window for good.
	Close()
}

// Tray is a status-area icon with a menu.
type Tray struct {
	Tooltip string
	// OnActivate runs on a click on the icon itself, where the platform
	// tells a click from opening the menu (Windows; on macOS and Linux
	// the menu opens).
	OnActivate func()
	Menu       []MenuItem
}

// MenuItem is a tray menu entry; a zero Label is a separator.
type MenuItem struct {
	Label    string
	Disabled bool
	OnClick  func()
}

// Info describes the native web view of this system.
type Info struct {
	// Engine is "WebView2", "WKWebView" or "WebKitGTK".
	Engine string
	// Version is the engine's version where the system says.
	Version string
}

var (
	probeFn func() (Info, error)
	runFn   func(Config, func(App)) error
)

// Register installs the native implementation (internal/webview does, from
// init).
func Register(probe func() (Info, error), run func(Config, func(App)) error) {
	probeFn, runFn = probe, run
}

// Probe reports whether Run can show native windows here, without
// creating any. ErrUnavailable (possibly wrapped) means it cannot.
func Probe() (Info, error) {
	if probeFn == nil {
		return Info{}, fmt.Errorf("%w (this program has no native windows)", ErrUnavailable)
	}
	return probeFn()
}

// Run starts the native application, calls setup on the UI thread and runs
// the event loop until App.Quit. It must be called from the main goroutine.
// If the system has no native web view it returns an error wrapping
// ErrUnavailable without calling setup.
func Run(cfg Config, setup func(App)) error {
	if runFn == nil {
		return fmt.Errorf("%w (this program has no native windows)", ErrUnavailable)
	}
	return runFn(cfg, setup)
}

// Size is the window size o asks for, with defaults.
func (o *Options) Size() (w, h int) {
	w, h = o.Width, o.Height
	if w <= 0 {
		w = 960
	}
	if h <= 0 {
		h = 680
	}
	return w, h
}
