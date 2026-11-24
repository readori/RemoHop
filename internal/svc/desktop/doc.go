// Package desktop is the RemoHop remote desktop. The host captures the screen
// and encodes it with an ffmpeg subprocess, AV1 or H.264 (GPU encoders when
// present, SVT-AV1, libx264 or libaom otherwise), streams it over the P2P
// session, and injects the client's mouse and keyboard input (XTEST on X11,
// SendInput on Windows). The client side is a Go API (Open, Session, Probe,
// Recorder) and the `remohop <ID> desktop` command, which serves a WebCodecs
// browser viewer on loopback and can record the stream to MP4.
package desktop
