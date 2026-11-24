// Package unattended gives the Windows service (`remohop service install
// --unattended`) the interactive console session: its screen, including the
// sign-in screen, the lock screen and UAC prompts on the secure desktop, and
// its keyboard and mouse.
//
// A Windows service runs as LocalSystem in session 0, which has no display.
// The service therefore stays the network endpoint and does the encoding,
// and for anything that needs the desktop it starts a helper process: the
// same remohop binary (`remohop service session-helper`), with a copy of the
// service's own token moved into the console session, started on
// winsta0\Winlogon. The helper's capture and input threads attach to
// whichever desktop currently receives input (Default, Winlogon, the screen
// saver) before every operation, so they follow the user from the desktop to
// the lock screen to a UAC prompt and back. As LocalSystem on the input
// desktop its SendInput is not filtered by UIPI, and GDI capture works on the
// secure desktop where the Desktop Duplication API and ffmpeg's grabbers do
// not reach.
//
// Service and helper talk over a named pipe the helper creates for its
// launch only: a random name, an access list that admits LocalSystem alone,
// remote clients rejected. Each end checks the process ID of the other
// (the helper accepts only the service that started it; the service only
// the helper it started) and both prove knowledge of a per-launch secret,
// which the service hands the helper through an inherited anonymous pipe
// (its standard input), never on the command line. The helper refuses to run
// unless it is LocalSystem, so nobody else can start one usefully.
//
// The service follows the console session: when the user logs off, another
// user takes over through fast user switching, or a remote desktop
// connection moves the session away from the console, the helper of the old
// session is ended and the next request starts one in the new console
// session. Helpers are started on demand and end when idle.
//
// Ctrl+Alt+Del is raised by the service itself with SendSAS, which Windows
// allows only when the SoftwareSASGeneration policy permits services.
package unattended

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/svc/screenshot"
)

// SessionEnv selects which session the service shows: "console" (the
// default) is the physical console, whatever is on it (a user's desktop or
// the sign-in screen); "active" prefers a session with a user logged in,
// the console's if it has one, else a remote desktop session.
const SessionEnv = "REMOHOP_UNATTENDED_SESSION"

// pipePrefix starts every helper pipe's name; a random suffix follows.
const pipePrefix = `\\.\pipe\remohop-ua-`

var pipeNameRE = regexp.MustCompile(`^\\\\\.\\pipe\\remohop-ua-[0-9a-f]{32}$`)

// Message types on the pipe. Every message is a type byte, a big-endian
// uint32 length and that many bytes.
const (
	msgRequest = 'Q' // service -> helper: JSON request
	msgReply   = 'R' // helper -> service: JSON reply
	msgData    = 'D' // helper -> service: pixels after a grab reply
	msgFrame   = 'F' // stream: a new frame (BGRX, top-down)
	msgSame    = 'S' // stream: the frame did not change
	msgEnd     = 'E' // stream: ended, JSON reply with the reason
)

// maxMsg bounds one message: a 8K screen of 32-bit pixels and some.
const maxMsg = 160 << 20

// maxJSON bounds a JSON message.
const maxJSON = 1 << 20

func writeMsg(w io.Writer, typ byte, payload []byte) error {
	var hdr [5]byte
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) == 0 {
		return nil
	}
	_, err := w.Write(payload)
	return err
}

func writeJSON(w io.Writer, typ byte, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return writeMsg(w, typ, b)
}

// readMsg reads one message, reusing buf when it is large enough.
func readMsg(r io.Reader, buf []byte) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxMsg || (hdr[0] != msgData && hdr[0] != msgFrame && n > maxJSON) {
		return 0, nil, fmt.Errorf("unattended: message of %d bytes is too large", n)
	}
	if cap(buf) < int(n) {
		buf = make([]byte, n)
	}
	buf = buf[:n]
	if _, err := io.ReadFull(r, buf); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return 0, nil, err
	}
	return hdr[0], buf, nil
}

func readJSON(r io.Reader, want byte, v any) error {
	typ, b, err := readMsg(r, nil)
	if err != nil {
		return err
	}
	if typ != want {
		return fmt.Errorf("unattended: got message %q, want %q", typ, want)
	}
	return json.Unmarshal(b, v)
}

// rect is an area of the virtual screen in pixels.
type rect struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// request is what the service asks of the helper.
type request struct {
	Op string `json:"op"`
	// Metric: GetSystemMetrics index.
	Index int `json:"index,omitempty"`
	// VkKeyScan / MapVirtualKey arguments.
	Ch      uint16 `json:"ch,omitempty"`
	Code    uint32 `json:"code,omitempty"`
	MapType uint32 `json:"map_type,omitempty"`
	// SendInput: INPUT structures back to back.
	Events []byte `json:"events,omitempty"`
	Count  int    `json:"count,omitempty"`
	Size   int    `json:"size,omitempty"`
	// Grab and stream: the area, the frame rate, whether to draw the
	// pointer.
	Rect   *rect `json:"rect,omitempty"`
	FPS    int   `json:"fps,omitempty"`
	Cursor bool  `json:"cursor,omitempty"`
}

// Operations.
const (
	opRPC      = "rpc"    // first request of a connection that carries calls
	opStream   = "stream" // first request of a frame stream connection
	opGrab     = "grab"   // first request of a one-shot capture connection
	opDisplays = "displays"
	opMetric   = "metric"
	opCursor   = "cursor"
	opVkScan   = "vkscan"
	opMapVK    = "mapvk"
	opInput    = "input"
	opLock     = "lock"
	opDesktop  = "desktop"
)

// reply is the helper's answer.
type reply struct {
	Error    string               `json:"error,omitempty"`
	Value    int64                `json:"value,omitempty"`
	X        int                  `json:"x,omitempty"`
	Y        int                  `json:"y,omitempty"`
	W        int                  `json:"w,omitempty"`
	H        int                  `json:"h,omitempty"`
	Displays []screenshot.Display `json:"displays,omitempty"`
	// Desktop is the name of the input desktop the helper is on.
	Desktop string `json:"desktop,omitempty"`
	Session uint32 `json:"session,omitempty"`
}

func (r reply) err() error {
	if r.Error == "" {
		return nil
	}
	return errors.New(r.Error)
}

// Handshake: the helper (pipe server) sends a nonce; the service answers
// with its own nonce and a MAC over both; the helper answers with a MAC in
// the other direction. Each side thereby proves it knows the launch secret
// without sending it, and neither MAC can be replayed into another
// connection.
const (
	nonceLen   = 32
	macLen     = sha256.Size
	labelSvc   = "remohop unattended: service"
	labelHelpr = "remohop unattended: helper"
)

var errAuth = errors.New("unattended: the other end of the pipe does not know the launch secret")

func handshakeMAC(secret []byte, label string, a, b []byte) []byte {
	m := hmac.New(sha256.New, secret)
	m.Write([]byte(label))
	m.Write(a)
	m.Write(b)
	return m.Sum(nil)
}

// serverHandshake is the helper's side.
func serverHandshake(rw io.ReadWriter, secret []byte) error {
	ns := make([]byte, nonceLen)
	if _, err := rand.Read(ns); err != nil {
		return err
	}
	if _, err := rw.Write(ns); err != nil {
		return err
	}
	in := make([]byte, nonceLen+macLen)
	if _, err := io.ReadFull(rw, in); err != nil {
		return err
	}
	nc, mac := in[:nonceLen], in[nonceLen:]
	if !hmac.Equal(mac, handshakeMAC(secret, labelSvc, ns, nc)) {
		return errAuth
	}
	_, err := rw.Write(handshakeMAC(secret, labelHelpr, nc, ns))
	return err
}

// clientHandshake is the service's side.
func clientHandshake(rw io.ReadWriter, secret []byte) error {
	ns := make([]byte, nonceLen)
	if _, err := io.ReadFull(rw, ns); err != nil {
		return err
	}
	nc := make([]byte, nonceLen)
	if _, err := rand.Read(nc); err != nil {
		return err
	}
	if _, err := rw.Write(append(append([]byte{}, nc...), handshakeMAC(secret, labelSvc, ns, nc)...)); err != nil {
		return err
	}
	mac := make([]byte, macLen)
	if _, err := io.ReadFull(rw, mac); err != nil {
		return err
	}
	if !hmac.Equal(mac, handshakeMAC(secret, labelHelpr, nc, ns)) {
		return errAuth
	}
	return nil
}

// helperConfig is what the service writes to the helper's standard input,
// as one JSON line.
type helperConfig struct {
	Pipe    string `json:"pipe"`
	Secret  string `json:"secret"` // hex, 32 bytes
	Parent  uint32 `json:"parent"` // the service's process ID
	Session uint32 `json:"session"`
}

// newHelperConfig makes a fresh pipe name and secret.
func newHelperConfig(parent, session uint32) (helperConfig, error) {
	var b [16 + 32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return helperConfig{}, err
	}
	return helperConfig{
		Pipe:    pipePrefix + hex.EncodeToString(b[:16]),
		Secret:  hex.EncodeToString(b[16:]),
		Parent:  parent,
		Session: session,
	}, nil
}

// parseHelperConfig reads and checks the line the service sent.
func parseHelperConfig(line []byte) (helperConfig, []byte, error) {
	var c helperConfig
	if err := json.Unmarshal(bytes.TrimSpace(line), &c); err != nil {
		return c, nil, fmt.Errorf("bad helper configuration: %w", err)
	}
	if !pipeNameRE.MatchString(c.Pipe) {
		return c, nil, fmt.Errorf("bad helper pipe name %q", c.Pipe)
	}
	secret, err := hex.DecodeString(c.Secret)
	if err != nil || len(secret) != 32 {
		return c, nil, errors.New("bad helper secret")
	}
	if c.Parent == 0 {
		return c, nil, errors.New("bad helper parent process ID")
	}
	return c, secret, nil
}

// Session states (WTS_CONNECTSTATE_CLASS).
const (
	wtsActive       = 0
	wtsConnected    = 1
	wtsDisconnected = 4
)

// noSession is what WTSGetActiveConsoleSessionId returns while the console
// is being attached to another session.
const noSession = 0xFFFFFFFF

type sessionInfo struct {
	ID    uint32
	State uint32
	Name  string // window station name: "Console", "RDP-Tcp#3", "Services"
}

// pickSession chooses the session to show: see SessionEnv. ok is false
// when there is none right now (the console is between sessions).
func pickSession(mode string, console uint32, sessions []sessionInfo) (uint32, bool) {
	valid := func(id uint32) bool { return id != 0 && id != noSession }
	if strings.EqualFold(mode, "active") {
		for _, s := range sessions {
			if s.ID == console && s.State == wtsActive && valid(console) {
				return console, true
			}
		}
		best, found := uint32(0), false
		for _, s := range sessions {
			if valid(s.ID) && s.State == wtsActive && (!found || s.ID < best) {
				best, found = s.ID, true
			}
		}
		if found {
			return best, true
		}
	}
	return console, valid(console)
}

// relaunchDelay is how long to wait before starting a helper again after n
// consecutive failures.
func relaunchDelay(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	d := time.Second << min(n-1, 5)
	return min(d, 30*time.Second)
}

// DesktopKind describes a desktop name for people.
func DesktopKind(name string) string {
	switch strings.ToLower(name) {
	case "default":
		return "the user's desktop"
	case "winlogon":
		return "the secure desktop (sign-in, lock screen, Ctrl+Alt+Del, UAC)"
	case "screen-saver":
		return "the screen saver"
	case "disconnect":
		return "the session is disconnected"
	case "":
		return "unknown desktop"
	}
	return "desktop " + name
}

// sasAllowed reports whether the SoftwareSASGeneration policy value lets a
// service raise Ctrl+Alt+Del: 1 (services) or 3 (services and Ease of
// Access applications). Absent or anything else: no.
func sasAllowed(value uint32, present bool) bool {
	return present && (value == 1 || value == 3)
}

// SASPolicyHint says how to allow the service to send Ctrl+Alt+Del (kept
// short: viewers show at most 300 characters of a reason).
const SASPolicyHint = `Windows lets services send Ctrl+Alt+Del only if the policy "Disable or enable software Secure Attention Sequence" ` +
	`is Enabled for Services: HKLM\SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System, SoftwareSASGeneration=1`

// copyFrames reads a frame stream (after its header) from src and writes
// every frame to dst, repeating the last one where the helper said it did
// not change. frameSize is the size of one frame. It returns nil when the
// helper ends the stream normally.
func copyFrames(dst io.Writer, src io.Reader, frameSize int) error {
	var frame, buf []byte
	for {
		typ, b, err := readMsg(src, buf)
		if err != nil {
			return err
		}
		switch typ {
		case msgFrame:
			if len(b) != frameSize {
				return fmt.Errorf("unattended: frame of %d bytes, want %d", len(b), frameSize)
			}
			// Swap: the buffer just read becomes the current frame.
			frame, buf = b, frame
		case msgSame:
			if frame == nil {
				return errors.New("unattended: stream repeated a frame before sending one")
			}
		case msgEnd:
			var r reply
			if err := json.Unmarshal(b, &r); err != nil {
				return err
			}
			return r.err()
		default:
			return fmt.Errorf("unattended: unexpected message %q in a frame stream", typ)
		}
		if _, err := dst.Write(frame); err != nil {
			return err
		}
	}
}

func displayRect(d screenshot.Display) rect { return rect{X: d.X, Y: d.Y, W: d.Width, H: d.Height} }

func union(a, b rect) rect {
	x0, y0 := min(a.X, b.X), min(a.Y, b.Y)
	x1, y1 := max(a.X+a.W, b.X+b.W), max(a.Y+a.H, b.Y+b.H)
	return rect{X: x0, Y: y0, W: x1 - x0, H: y1 - y0}
}

// displayArea is the area of display index display (as the screenshot
// service numbers them), or of all of them (screenshot.AllDisplays).
func displayArea(ds []screenshot.Display, display int) (rect, error) {
	if len(ds) == 0 {
		return rect{}, errors.New("no display available in the console session")
	}
	if display == screenshot.AllDisplays {
		u := displayRect(ds[0])
		for _, d := range ds[1:] {
			u = union(u, displayRect(d))
		}
		return u, nil
	}
	if display < 0 || display >= len(ds) {
		return rect{}, fmt.Errorf("no display %d (the console session has %d)", display, len(ds))
	}
	return displayRect(ds[display]), nil
}

// rectMatches reports whether r is one of the displays, or their bounding
// box (a capture of all of them).
func rectMatches(r rect, ds []screenshot.Display) bool {
	if len(ds) == 0 {
		return false
	}
	u := displayRect(ds[0])
	for _, d := range ds {
		dr := displayRect(d)
		if dr == r {
			return true
		}
		u = union(u, dr)
	}
	return u == r
}
