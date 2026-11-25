// Package dpiaware makes a Windows process see the screen in physical
// pixels on every monitor.
//
// A process that is not DPI aware gets every coordinate scaled to 96 dpi;
// one that is system DPI aware (SetProcessDPIAware, what remohop used)
// gets physical pixels only for monitors at the primary monitor's scale,
// and scaled rectangles for the others. With monitors at different scales
// (a 4K laptop panel at 200% next to a 1440p screen at 100%) the capture
// area, the pointer mapping and the size of the picture then disagree.
// Per-monitor awareness (v2) gives physical pixels everywhere, which is
// also what Desktop Duplication and GDI capture: a 4K monitor is streamed
// at 3840x2160, not at its scaled size.
package dpiaware

import (
	"sync"

	"golang.org/x/sys/windows"
)

var (
	user32                        = windows.NewLazySystemDLL("user32.dll")
	shcore                        = windows.NewLazySystemDLL("shcore.dll")
	procSetProcessDpiAwarenessCtx = user32.NewProc("SetProcessDpiAwarenessContext")
	procSetProcessDpiAwareness    = shcore.NewProc("SetProcessDpiAwareness")
	procSetProcessDPIAware        = user32.NewProc("SetProcessDPIAware")
	once                          sync.Once
	level                         string
)

// Enable makes the process DPI aware, as much as this Windows allows:
// per-monitor v2 (Windows 10 1703+), per-monitor (8.1+), system (Vista+).
// The awareness of a process can be set once; later calls do nothing. It
// returns the level in effect ("" if none could be set, for instance
// because something set it before).
func Enable() string {
	once.Do(func() {
		const perMonitorV2 = ^uintptr(3) // DPI_AWARENESS_CONTEXT_PER_MONITOR_AWARE_V2 (-4)
		if procSetProcessDpiAwarenessCtx.Find() == nil {
			if r, _, _ := procSetProcessDpiAwarenessCtx.Call(perMonitorV2); r != 0 {
				level = "per-monitor-v2"
				return
			}
		}
		if procSetProcessDpiAwareness.Find() == nil {
			if r, _, _ := procSetProcessDpiAwareness.Call(2 /* PROCESS_PER_MONITOR_DPI_AWARE */); r == 0 { // S_OK
				level = "per-monitor"
				return
			}
		}
		if r, _, _ := procSetProcessDPIAware.Call(); r != 0 {
			level = "system"
		}
	})
	return level
}
