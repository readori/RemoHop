// Package x11disp is the single implementation of "which X display should
// this daemon use, and how does it authenticate to it".
//
// A host daemon is often started from systemd or SSH, without DISPLAY or
// XAUTHORITY, while a desktop session (or several stale X sockets and a live
// Xvfb) exists on the machine. Find resolves that the way a person would:
// an explicit DISPLAY/XAUTHORITY first, then every local X socket in turn,
// each tried with every cookie found in the usual Xauthority locations,
// until one really accepts the connection. Sockets that refuse connections
// are skipped. The result can open authenticated xgb connections and
// produces the environment child processes (ffmpeg, xclip) need.
package x11disp
