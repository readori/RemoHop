// Package webview shows web pages in native windows, with a tray icon, from a
// pure Go program: WebView2 on Windows (through the COM interface, see
// github.com/jchv/go-webview2), WKWebView on macOS and WebKitGTK on Linux
// (both through purego, without cgo). See docs/desktop-app.md for why and
// for what each platform can do.
//
// Only the RemoHop desktop app (cmd/remohop-quick) links this package. On
// Linux purego makes a CGO_ENABLED=0 binary dynamically linked against the
// C library (it has to load GTK), so the remohop command line tool, which
// must stay a static binary, never imports it; cmd/remohop's tests check
// that.
//
// # Threads
//
// Every windowing system here wants its windows created and driven from
// one thread, and macOS insists on the process's first thread. Importing
// this package locks the main goroutine to that thread (an init function
// calling runtime.LockOSThread), so Run must be called from the main
// goroutine: cli.Main runs commands there. Everything an App does must run
// on that thread too: in Run's setup function or in a function given to
// App.Dispatch, which may be called from any goroutine.
package webview

import (
	"runtime"

	"github.com/readori/RemoHop/internal/nativeui"
)

func init() { runtime.LockOSThread() }

// fitArea fits a window into the work area (nativeui.FitArea).
var fitArea = nativeui.FitArea

// The types are nativeui's; this package implements them.
type (
	Config   = nativeui.Config
	App      = nativeui.App
	Options  = nativeui.Options
	Window   = nativeui.Window
	Tray     = nativeui.Tray
	MenuItem = nativeui.MenuItem
	Info     = nativeui.Info
)

// ErrUnavailable means this system has no usable native web view.
var ErrUnavailable = nativeui.ErrUnavailable

func init() { nativeui.Register(probe, run) }

// overlayMargin is the banner's distance from the work area's edges, in
// logical pixels.
const overlayMargin = 12

// Probe reports whether Run can show native windows here, without
// creating any. ErrUnavailable (possibly wrapped) means it cannot.
func Probe() (Info, error) { return probe() }

// Run starts the native application, calls setup on the UI thread and
// runs the event loop until App.Quit. It must be called from the main
// goroutine (see the package comment). If the system has no native web
// view it returns an error wrapping ErrUnavailable without calling setup.
func Run(cfg Config, setup func(App)) error { return run(cfg, setup) }
