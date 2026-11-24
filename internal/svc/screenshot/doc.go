// Package screenshot is the RemoHop screenshot service: it captures the
// remote machine's screen so that people and multimodal models can look at
// UI state or error dialogs.
//
// Wire protocol on one "screenshot" stream: the client sends a JSON request
// (op "capture" or "list"), the host answers with one JSON header carrying
// either an error or the image metadata, and for a capture the header is
// followed by exactly Size bytes of PNG or JPEG data. The host captures,
// crops to the requested region (echoed in the header, so a client can tell
// a host that ignored it), downscales (box filter) and encodes, so only the
// final image crosses the network. Sizes are bounded on both ends.
//
// Capture backends on the host: pure-Go X11 (GetImage on the root window) on
// Linux, with fallbacks to grim, gnome-screenshot and spectacle on Wayland;
// screencapture on macOS; GDI on Windows.
package screenshot
