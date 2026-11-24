// Package input is the RemoHop input service: single pointer and keyboard
// actions on the remote desktop (click, move, type, key, scroll), so that
// people and AI agents can drive a GUI from the command line in an
// observe (screenshot), act, verify loop.
//
// Coordinates are pixels of a display exactly as the screenshot service
// captures it at full size, origin top-left; the host translates them to
// its virtual screen with the monitor geometry the screenshot service
// reports. Target.ScaleFrom scales coordinates taken from a downscaled
// screenshot.
//
// Wire protocol: one "input" stream per action. The request travels in the
// stream header ({"op": "click"|"move"|"type"|"key"|"scroll", ...}); the
// host validates it (sizes and key names are bounded and checked on both
// ends), performs it and answers one JSON message, {"error": "..."} or
// {"result": {...}}. The client writes nothing else; resetting the stream
// aborts a long action. Requests are serialised on the host, and every key
// and button is released when a request ends, however it ends.
//
// The host refuses input when REMOHOP_DESKTOP_VIEW_ONLY is set (as the desktop
// viewer does). Backends live in internal/inject: XTEST on Linux/X11,
// SendInput on Windows, Quartz events (CGEventPost, loaded with purego, no
// cgo) on macOS, where the host needs the Accessibility permission and
// coordinates are converted from screenshot pixels to display points.
package input
