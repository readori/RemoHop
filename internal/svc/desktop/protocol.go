package desktop

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Wire protocol of the "desktop" service.
//
// The client opens one stream with an Options request. From then on:
//
//	host -> client: wire frames
//	  'H' JSON Hello       before the first video frame and again whenever
//	                       the encoder restarts with different parameters,
//	                       or the host's owner switches input off or on
//	                       (Hello.Input; the encoder is restarted for it)
//	  'V' video            frameHeaderLen-byte header + one frame: an AV1
//	                       temporal unit, or an H.264 access unit as
//	                       length-prefixed NAL units (as in MP4)
//	  'S' JSON HostStats   about once a second
//	  'P' JSON pong        answer to a ping, for clock-offset estimation
//	  'R' JSON Reply       outcome of a control that carried an id
//	  'C' JSON cursorMsg   the pointer's position, visibility and shape,
//	                       only after a Hello with Cursor set (see below)
//	  'E' JSON {"error"}   fatal error; the host closes the stream after it
//	client -> host: wire JSON messages (Control)
//	  ack, keyframe, behind, config, ping, cursor, input events, and the
//	  host controls type, lock, block_input and blackout
//
// A request with Probe set is answered with one 'I' frame (JSON HostInfo)
// and nothing is captured.
//
// Flow control is by acknowledgement: the client acks each video frame once
// it has consumed it (decoded, for the browser viewer), and the host never
// lets more than a bounded number of frames be unacknowledged. When that
// window is full the host drops delta frames, waits for the backlog to drain
// and then restarts from a key frame, so latency stays bounded on any link
// and with any slow consumer instead of queueing without limit.
//
// The pointer. By default the host draws the mouse pointer into the video,
// so it moves only as fast as frames come back. A client that draws the
// pointer itself (the browser viewer, with a CSS cursor that moves at the
// local mouse's own rate) asks with Options.Cursor. A host that can read
// its pointer then leaves it out of the video, says so with Hello.Cursor,
// and sends 'C' frames: the pointer's shape when it changes (an image sent
// once per session and later named by its ID) and its position and
// visibility when they change. A host that cannot (an older host, macOS,
// the Windows service's console capture, a pointer it fails to read) keeps
// drawing it and never sets Hello.Cursor; a client that did not ask gets
// the pointer in the video as before. The "cursor" control turns it off
// and on again mid-session (the viewer's recordings need the pointer in
// the picture).
//
// Relative mouse. A host that sets Hello.Relative takes "move_by" input
// events (the movement of a pointer locked by the client, for games and
// 3D views) and buttons with Rel set, which act where the pointer is
// instead of moving it first. Like every input event they need the
// session to have input; an older host ignores them, so a client sends
// them only to a host that says it takes them.

// ServiceName is the peer service the host registers.
const ServiceName = "desktop"

const (
	frameHello = 'H'
	frameVideo = 'V'
	frameStats = 'S'
	framePong  = 'P'
	frameReply = 'R'
	frameInfo  = 'I'
	// frameCursor carries a cursorMsg (see "The pointer" above).
	frameCursor = 'C'
	frameError  = 'E'
)

// Video frame header layout (big-endian):
//
//	[0]      flags (bit 0: key frame)
//	[1:5]    sequence number, per session, starting at 1
//	[5:13]   pts in microseconds (monotonic within one encoder generation)
//	[13:21]  capture time, Unix microseconds on the host clock (0: unknown)
//	[21:29]  host send time, Unix microseconds on the host clock
const frameHeaderLen = 29

const flagKey = 1

type frameHeader struct {
	key     bool
	seq     uint32
	pts     int64
	capture int64
	sent    int64
}

func (h frameHeader) put(b []byte) {
	if h.key {
		b[0] = flagKey
	} else {
		b[0] = 0
	}
	binary.BigEndian.PutUint32(b[1:5], h.seq)
	binary.BigEndian.PutUint64(b[5:13], uint64(h.pts))
	binary.BigEndian.PutUint64(b[13:21], uint64(h.capture))
	binary.BigEndian.PutUint64(b[21:29], uint64(h.sent))
}

func parseFrameHeader(b []byte) (frameHeader, error) {
	if len(b) < frameHeaderLen {
		return frameHeader{}, errors.New("desktop: short video frame")
	}
	return frameHeader{
		key:     b[0]&flagKey != 0,
		seq:     binary.BigEndian.Uint32(b[1:5]),
		pts:     int64(binary.BigEndian.Uint64(b[5:13])),
		capture: int64(binary.BigEndian.Uint64(b[13:21])),
		sent:    int64(binary.BigEndian.Uint64(b[21:29])),
	}, nil
}

// Options is the client's request: what to capture and how to encode it.
// Zero values mean "host default".
type Options struct {
	// FPS is the requested frame rate (default 60, max 240). A software
	// encoder that cannot keep up makes the host lower it.
	FPS int `json:"fps,omitempty"`
	// Bitrate in bits per second; 0 picks one from resolution and frame
	// rate and lets the host adapt it to the link.
	Bitrate int64 `json:"bitrate,omitempty"`
	// MaxHeight downscales (keeping the aspect ratio) so the video is at
	// most this many pixels high. 0 keeps the native size.
	MaxHeight int `json:"max_height,omitempty"`
	// Scale downscales by a factor in (0, 1]. 0 means 1.
	Scale float64 `json:"scale,omitempty"`
	// Display selects a monitor: DisplayAll (-1) is every monitor in one
	// picture (the virtual desktop), 0 the host's default (X11: the entire
	// root window, which is every monitor; Windows/macOS: the primary
	// display), N >= 1 the Nth monitor of HostInfo.Displays.
	Display int `json:"display,omitempty"`
	// ViewOnly asks the host not to accept input on this session.
	ViewOnly bool `json:"view_only,omitempty"`
	// Encoder forces a specific ffmpeg encoder (e.g. "libsvtav1") instead
	// of the host's automatic choice.
	Encoder string `json:"encoder,omitempty"`

	// Codec restricts the video codec to "av1" or "h264"; empty lets the
	// host choose (AV1 with a GPU AV1 encoder, else H.264; software AV1 only
	// for a viewer that decodes nothing else).
	Codec string `json:"codec,omitempty"`
	// Decode lists the codecs the viewer can decode ("av1", "h264"); empty
	// means both. The host never picks a codec outside it.
	Decode []string `json:"decode,omitempty"`
	// Probe asks only for the host's HostInfo; see Probe.
	Probe bool `json:"probe,omitempty"`
	// Cursor says the client draws the mouse pointer itself: the host may
	// then leave it out of the video and send its shape and position
	// instead (Hello.Cursor says whether it does).
	Cursor bool `json:"cursor,omitempty"`

	// ManualAck makes the client application acknowledge frames itself
	// with Session.Ack (the browser viewer acks after decoding). By default
	// Session.Next acknowledges each frame it returns. Not sent to the host.
	ManualAck bool `json:"-"`
}

const (
	defaultFPS = 60
	maxFPS     = 240
	minBitrate = 200_000
	maxBitrate = 400_000_000
	maxDisplay = 64
	// DisplayAll selects every monitor at once; see Options.Display.
	DisplayAll = -1
)

// normalize validates untrusted options and fills in defaults.
func (o *Options) normalize() error {
	switch {
	case o.FPS == 0:
		o.FPS = defaultFPS
	case o.FPS < 1 || o.FPS > maxFPS:
		return fmt.Errorf("fps %d out of range 1..%d", o.FPS, maxFPS)
	}
	if o.Bitrate != 0 && (o.Bitrate < minBitrate || o.Bitrate > maxBitrate) {
		return fmt.Errorf("bitrate %d out of range %d..%d", o.Bitrate, minBitrate, maxBitrate)
	}
	if o.MaxHeight != 0 && (o.MaxHeight < 64 || o.MaxHeight > 8640) {
		return fmt.Errorf("max height %d out of range 64..8640", o.MaxHeight)
	}
	if o.Scale == 0 {
		o.Scale = 1
	}
	if math.IsNaN(o.Scale) || o.Scale < 0.05 || o.Scale > 1 {
		return fmt.Errorf("scale %v out of range 0.05..1", o.Scale)
	}
	if o.Display < DisplayAll || o.Display > maxDisplay {
		return fmt.Errorf("display %d out of range %d..%d", o.Display, DisplayAll, maxDisplay)
	}
	if o.Encoder != "" && !knownEncoder(o.Encoder) {
		return fmt.Errorf("unknown encoder %q", o.Encoder)
	}
	if !validCodec(o.Codec) && o.Codec != "" {
		return fmt.Errorf("unknown codec %q (want av1 or h264)", o.Codec)
	}
	if len(o.Decode) > 8 {
		return errors.New("too many decodable codecs")
	}
	for _, c := range o.Decode {
		if !validCodec(c) {
			return fmt.Errorf("unknown codec %q (want av1 or h264)", c)
		}
	}
	return nil
}

func validCodec(c string) bool { return c == codecAV1 || c == codecH264 }

// DisplayInfo is one monitor of the host, in screen pixels.
type DisplayInfo struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Names of the host controls, as used in Hello.Unsupported.
const (
	ControlType       = "type"         // type text
	ControlLock       = "lock"         // lock the screen
	ControlBlockInput = "block_input"  // disable the physical keyboard and mouse
	ControlBlackout   = "blackout"     // hide the physical screen
	ControlCtrlAltDel = "ctrl_alt_del" // the secure attention sequence
	// ControlAllMonitors is the all-monitors view (DisplayAll).
	ControlAllMonitors = "all_monitors"
)

// HostInfo is the answer to a probe: what a session on this host would
// offer, without starting one.
type HostInfo struct {
	OS string `json:"os"`
	// ScreenWidth/ScreenHeight are the whole screen in pixels (0 if the
	// host does not know); Displays are its monitors, when known.
	ScreenWidth  int           `json:"screen_width,omitempty"`
	ScreenHeight int           `json:"screen_height,omitempty"`
	Displays     []DisplayInfo `json:"displays,omitempty"`
	// Encoders maps each codec the host can encode ("av1", "h264") to the
	// encoder it would use.
	Encoders map[string]string `json:"encoders"`
	// Input reports whether a session could send input; InputError says
	// why not.
	Input      bool   `json:"input"`
	InputError string `json:"input_error,omitempty"`
	// Unsupported maps the controls this host cannot offer to the reason.
	Unsupported map[string]string `json:"unsupported,omitempty"`
	Warning     string            `json:"warning,omitempty"`
}

// Hello describes the video stream that follows.
type Hello struct {
	Width   int   `json:"width"`
	Height  int   `json:"height"`
	FPS     int   `json:"fps"`
	Bitrate int64 `json:"bitrate"`
	// Codec is the WebCodecs codec string: av01.P.LLT.DD for AV1,
	// avc1.PPCCLL for H.264.
	Codec string `json:"codec"`
	// CodecConfig is the codec's MP4 configuration record: av1C for AV1,
	// avcC for H.264 (which is also the WebCodecs "description" that makes
	// a decoder take length-prefixed NAL units).
	CodecConfig []byte `json:"codec_config,omitempty"`
	Encoder     string `json:"encoder"`
	// Hardware is true for GPU encoders.
	Hardware bool `json:"hardware"`
	// ScreenWidth/ScreenHeight are the captured area in host pixels, when
	// the host knows them.
	ScreenWidth  int `json:"screen_width,omitempty"`
	ScreenHeight int `json:"screen_height,omitempty"`
	// Input reports whether the host accepts input on this session;
	// InputError says why not. It can change during the session: when
	// the host's owner switches keyboard and mouse off, the host releases
	// whatever the client holds, ignores input from then on and sends a
	// new Hello with Input false; switched back on, another Hello brings
	// input back.
	Input      bool   `json:"input"`
	InputError string `json:"input_error,omitempty"`
	// Warning is a non-fatal problem worth showing (e.g. capturing an
	// XWayland display).
	Warning string `json:"warning,omitempty"`
	// Display is the monitor requested (see Options.Display) and Displays
	// the host's monitors, when it knows them. AllDisplays is true when the
	// picture covers every monitor, however that was requested.
	Display     int           `json:"display"`
	Displays    []DisplayInfo `json:"displays,omitempty"`
	AllDisplays bool          `json:"all_displays,omitempty"`
	// Unsupported maps the host controls (Control* names) that this host
	// cannot offer to the reason why.
	Unsupported map[string]string `json:"unsupported,omitempty"`
	// Generation counts encoder (re)starts.
	Generation int `json:"generation"`
	// Cursor is true when the video of this generation leaves the mouse
	// pointer out and the host sends it in 'C' frames instead, for a
	// client that asked with Options.Cursor. ScreenWidth and ScreenHeight
	// are then set: cursor positions and images are in those pixels.
	Cursor bool `json:"cursor,omitempty"`
	// Relative is true when the host takes relative pointer movement
	// (InputMoveBy) and buttons that act where the pointer is (Rel).
	Relative bool `json:"relative,omitempty"`
}

// IsH264 reports whether the stream is H.264 (otherwise it is AV1).
func (h Hello) IsH264() bool { return len(h.Codec) > 4 && h.Codec[:4] == "avc1" }

// Reply is the host's answer to a control sent with an id.
type Reply struct {
	ID    uint32 `json:"id"`
	Type  string `json:"type"`
	Error string `json:"error,omitempty"`
}

// HostStats is the host's periodic report.
type HostStats struct {
	FPS        float64 `json:"fps"`         // frames sent per second
	Bitrate    int64   `json:"bitrate"`     // current encoder target, bits/s
	SendRate   int64   `json:"send_rate"`   // bits/s actually sent
	Dropped    uint64  `json:"dropped"`     // frames dropped so far
	InFlight   int     `json:"in_flight"`   // unacknowledged frames
	PipelineMS float64 `json:"pipeline_ms"` // capture -> encoded, when known
	Restarts   int     `json:"restarts"`    // encoder restarts so far
	// ForcedKeys counts the key frames the running encoder was asked for
	// (served without a restart, see keyforce.go).
	ForcedKeys int `json:"forced_keys,omitempty"`
	// AckMS is the smoothed time from sending a frame to its ack (round
	// trip plus the viewer's decode), WindowMS how much video may be
	// unacknowledged (see flow.go).
	AckMS float64 `json:"ack_ms,omitempty"`
	// Paused says why the host cannot capture its screen right now (the
	// session goes on and the picture resumes when it can; "" otherwise).
	// On Windows: the secure desktop of a UAC prompt, the lock screen or
	// Ctrl+Alt+Del, which only the unattended service can capture.
	Paused   string  `json:"paused,omitempty"`
	WindowMS float64 `json:"window_ms,omitempty"`
}

type pong struct {
	T    int64 `json:"t"`    // client time echoed back
	Host int64 `json:"host"` // host Unix microseconds when answering
}

type errorMsg struct {
	Error string `json:"error"`
}

// Control is one client -> host message. Type selects which fields matter.
type Control struct {
	Type string `json:"type"`

	// ID, when not zero, makes the host answer with a Reply.
	ID uint32 `json:"id,omitempty"`

	Seq uint32 `json:"seq,omitempty"` // ack
	T   int64  `json:"t,omitempty"`   // ping: client clock, echoed

	// config
	FPS       int     `json:"fps,omitempty"`
	Bitrate   int64   `json:"bitrate,omitempty"`
	MaxHeight int     `json:"max_height,omitempty"`
	Scale     float64 `json:"scale,omitempty"`
	Display   *int    `json:"display,omitempty"`

	// host controls
	Text string `json:"text,omitempty"` // type
	On   bool   `json:"on,omitempty"`   // block_input, blackout

	// input; see InputEvent
	X      float64 `json:"x,omitempty"`
	Y      float64 `json:"y,omitempty"`
	Button int     `json:"button,omitempty"`
	Down   bool    `json:"down,omitempty"`
	DX     float64 `json:"dx,omitempty"`
	DY     float64 `json:"dy,omitempty"`
	Code   string  `json:"code,omitempty"`
	Rel    bool    `json:"rel,omitempty"`
}

// Control types.
const (
	ctlAck      = "ack"
	ctlKeyframe = "keyframe"
	ctlConfig   = "config"
	ctlPing     = "ping"
	// ctlBehind: the consumer fell behind and is skipping to the next key
	// frame. The host treats it like congestion.
	ctlBehind = "behind"
	// ctlCursor: On asks the host to leave the pointer out of the video
	// and send it in 'C' frames (as Options.Cursor does at the start),
	// off to draw it into the video again. The host restarts its encoder
	// to apply it; the next Hello says what it does.
	ctlCursor = "cursor"
	// Host controls; each is answered with a Reply.
	ctlType       = ControlType
	ctlLock       = ControlLock
	ctlBlockInput = ControlBlockInput
	ctlBlackout   = ControlBlackout
)

// maxTypeText bounds the text of one "type" control, in bytes.
const maxTypeText = 16 << 10

// Input event kinds (InputEvent.Kind and Control.Type).
const (
	InputMove       = "move"        // X, Y
	InputButton     = "button"      // Button, Down (and X, Y)
	InputWheel      = "wheel"       // DX, DY in notches (positive: right/down)
	InputKey        = "key"         // Code, Down
	InputReleaseAll = "release_all" // release every key/button held
	// InputMoveBy moves the pointer by DX, DY host pixels from where it
	// is, like a mouse (a pointer locked by the client). Only for a host
	// whose Hello sets Relative.
	InputMoveBy = "move_by"
)

// maxMoveBy bounds one relative move, in pixels on each axis.
const maxMoveBy = 4096

// InputEvent is one pointer or keyboard event.
type InputEvent struct {
	Kind string
	// X, Y are the pointer position as fractions of the video frame, 0..1
	// from the top-left, so they stay valid whatever the scaling.
	X, Y float64
	// Button follows DOM MouseEvent.button: 0 left, 1 middle, 2 right,
	// 3 back, 4 forward.
	Button int
	Down   bool
	// DX, DY are wheel notches (positive scrolls right/down), or for
	// InputMoveBy the movement in host pixels (positive: right/down).
	DX, DY float64
	// Code is a DOM KeyboardEvent.code value such as "KeyA" or "ShiftLeft".
	Code string
	// Rel makes a button act where the pointer is, without moving it to
	// X, Y first (a pointer locked by the client). Only for a host whose
	// Hello sets Relative.
	Rel bool
}

func (e InputEvent) control() Control {
	return Control{Type: e.Kind, X: e.X, Y: e.Y, Button: e.Button, Down: e.Down, DX: e.DX, DY: e.DY, Code: e.Code, Rel: e.Rel}
}

func (c Control) input() InputEvent {
	return InputEvent{Kind: c.Type, X: c.X, Y: c.Y, Button: c.Button, Down: c.Down, DX: c.DX, DY: c.DY, Code: c.Code, Rel: c.Rel}
}

func isInput(t string) bool {
	switch t {
	case InputMove, InputButton, InputWheel, InputKey, InputReleaseAll, InputMoveBy:
		return true
	}
	return false
}

// Config changes encoding parameters mid-session; zero fields keep the
// current value. The host restarts the encoder to apply it.
type Config struct {
	FPS       int
	Bitrate   int64
	MaxHeight int
	Scale     float64
	// Display switches to another monitor (see Options.Display; DisplayAll
	// shows every monitor); nil keeps the current one.
	Display *int
}
