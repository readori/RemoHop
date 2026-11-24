package input

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/inject"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/svc/screenshot"
	"github.com/readori/RemoHop/internal/wire"
)

// ServiceName is the stream service of single input actions.
const ServiceName = "input"

// Limits, enforced on both ends.
const (
	// MaxTextRunes and MaxTextBytes bound one Type call.
	MaxTextRunes = 10000
	MaxTextBytes = 64 << 10
	// MaxCombos bounds the combos (times Repeat) of one Key call.
	MaxCombos = 100
	// MaxScroll bounds the wheel notches of one Scroll call per axis.
	MaxScroll = 100
	// MaxDelay bounds the pause between characters or combos.
	MaxDelay = time.Second
	// maxDuration bounds how long one call may keep the host busy.
	maxDuration = 5 * time.Minute
	// DefaultTypeDelay paces typing so applications keep up (xdotool's
	// default too); DefaultKeyDelay separates consecutive combos.
	DefaultTypeDelay = 12 * time.Millisecond
	DefaultKeyDelay  = 30 * time.Millisecond
)

// Size is an image size, for Target.ScaleFrom.
type Size struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// ParseSize parses "WxH", for example "1280x720".
func ParseSize(s string) (Size, error) {
	w, h, ok := strings.Cut(strings.ToLower(strings.TrimSpace(s)), "x")
	wi, err1 := strconv.Atoi(w)
	hi, err2 := strconv.Atoi(h)
	if !ok || err1 != nil || err2 != nil || wi <= 0 || hi <= 0 || wi > 1<<16 || hi > 1<<16 {
		return Size{}, fmt.Errorf("bad size %q (want WIDTHxHEIGHT, for example 1280x720)", s)
	}
	return Size{wi, hi}, nil
}

func (s Size) String() string { return fmt.Sprintf("%dx%d", s.Width, s.Height) }

// Point is a pixel position.
type Point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

// Target says which display coordinates refer to and in what scale.
type Target struct {
	// Display selects the monitor: "" for the primary, an index or a name
	// as `remohop <ID> screenshot --list` prints them, or "all" for the whole
	// virtual screen (the image `screenshot --display all` gives).
	Display string
	// ScaleFrom, when set, says the coordinates are pixels of an image of
	// this size showing the whole display (typically a downscaled
	// screenshot); they are scaled to the display's real pixels. Zero means
	// coordinates are real display pixels.
	ScaleFrom Size
}

// ClickOptions configures Click.
type ClickOptions struct {
	Target
	Button string // "left" (default), "right" or "middle"
	Double bool   // double-click
}

// MoveOptions configures Move.
type MoveOptions struct {
	Target
}

// TypeOptions configures Type.
type TypeOptions struct {
	Target               // only used to report the pointer position
	Delay  time.Duration // pause after each character; 0 means DefaultTypeDelay
}

// KeyOptions configures Key.
type KeyOptions struct {
	Target               // only used to report the pointer position
	Repeat int           // press the whole sequence this many times (default 1)
	Delay  time.Duration // pause between combos; 0 means DefaultKeyDelay
}

// ScrollOptions configures Scroll.
type ScrollOptions struct {
	Target
	// At, if set, moves the pointer there first (scrolling goes to the
	// window under the pointer).
	At *Point
}

// Result describes what the host did.
type Result struct {
	Action string `json:"action"` // click, move, type, key or scroll
	// Display is the monitor the coordinates refer to, in virtual-screen
	// pixels.
	Display screenshot.Display `json:"display"`
	// ScaleFrom echoes Target.ScaleFrom when coordinates were scaled.
	ScaleFrom *Size `json:"scale_from,omitempty"`
	// At is where a pointer action happened, in display pixels after
	// scaling; Virtual is the same point on the virtual screen.
	At      *Point `json:"at,omitempty"`
	Virtual *Point `json:"virtual,omitempty"`

	Button string   `json:"button,omitempty"`
	Clicks int      `json:"clicks,omitempty"`
	Keys   []string `json:"keys,omitempty"`  // combos pressed, canonical spelling
	Chars  int      `json:"chars,omitempty"` // characters typed
	DX     int      `json:"dx,omitempty"`
	DY     int      `json:"dy,omitempty"`

	// Pointer is the final pointer position in display pixels (it may lie
	// outside the display, see PointerOnDisplay), PointerScaled the same in
	// ScaleFrom pixels, PointerVirtual on the virtual screen.
	Pointer          Point  `json:"pointer"`
	PointerScaled    *Point `json:"pointer_scaled,omitempty"`
	PointerVirtual   Point  `json:"pointer_virtual"`
	PointerOnDisplay bool   `json:"pointer_on_display"`

	Warning string `json:"warning,omitempty"`
}

// Operations on the wire.
const (
	opClick  = "click"
	opMove   = "move"
	opType   = "type"
	opKey    = "key"
	opScroll = "scroll"
)

// request is the stream header of one action.
type request struct {
	Op        string   `json:"op"`
	Display   string   `json:"display,omitempty"`
	ScaleFrom *Size    `json:"scale_from,omitempty"`
	At        *Point   `json:"at,omitempty"`
	Button    string   `json:"button,omitempty"`
	Clicks    int      `json:"clicks,omitempty"`
	Text      string   `json:"text,omitempty"`
	Keys      []string `json:"keys,omitempty"`
	Repeat    int      `json:"repeat,omitempty"`
	DX        int      `json:"dx,omitempty"`
	DY        int      `json:"dy,omitempty"`
	DelayMS   int      `json:"delay_ms,omitempty"`
}

type reply struct {
	Error  string  `json:"error,omitempty"`
	Result *Result `json:"result,omitempty"`
}

// Click moves the pointer to x, y and clicks.
func Click(ctx context.Context, conn *peer.Conn, x, y int, opts ClickOptions) (*Result, error) {
	return do(ctx, conn, clickReq(x, y, opts))
}

// Move moves the pointer to x, y.
func Move(ctx context.Context, conn *peer.Conn, x, y int, opts MoveOptions) (*Result, error) {
	return do(ctx, conn, moveReq(x, y, opts))
}

// Type types text into whatever has the keyboard focus. Any Unicode text
// works, whatever the host's keyboard layout; newlines press Return and
// tabs press Tab.
func Type(ctx context.Context, conn *peer.Conn, text string, opts TypeOptions) (*Result, error) {
	return do(ctx, conn, typeReq(text, opts))
}

// Key presses key combos in sequence, for example "ctrl+c", "Return" or
// "alt+F4". Modifiers are pressed in the order given and always released.
func Key(ctx context.Context, conn *peer.Conn, combos []string, opts KeyOptions) (*Result, error) {
	return do(ctx, conn, keyReq(combos, opts))
}

// Scroll turns the mouse wheel: dy notches down (negative: up) and dx
// notches right (negative: left).
func Scroll(ctx context.Context, conn *peer.Conn, dx, dy int, opts ScrollOptions) (*Result, error) {
	return do(ctx, conn, scrollReq(dx, dy, opts))
}

func targetReq(op string, t Target) request {
	req := request{Op: op, Display: t.Display}
	if t.ScaleFrom != (Size{}) {
		s := t.ScaleFrom
		req.ScaleFrom = &s
	}
	return req
}

func clickReq(x, y int, opts ClickOptions) request {
	req := targetReq(opClick, opts.Target)
	req.At, req.Button, req.Clicks = &Point{x, y}, opts.Button, 1
	if opts.Double {
		req.Clicks = 2
	}
	return req
}

func moveReq(x, y int, opts MoveOptions) request {
	req := targetReq(opMove, opts.Target)
	req.At = &Point{x, y}
	return req
}

func typeReq(text string, opts TypeOptions) request {
	req := targetReq(opType, opts.Target)
	req.Text, req.DelayMS = text, delayMS(opts.Delay)
	return req
}

func keyReq(combos []string, opts KeyOptions) request {
	req := targetReq(opKey, opts.Target)
	req.Keys, req.Repeat, req.DelayMS = combos, opts.Repeat, delayMS(opts.Delay)
	return req
}

func scrollReq(dx, dy int, opts ScrollOptions) request {
	req := targetReq(opScroll, opts.Target)
	req.DX, req.DY, req.At = dx, dy, opts.At
	return req
}

// delayMS encodes a delay for the wire: 0 means the default, so an
// explicit zero delay travels as -1.
func delayMS(d time.Duration) int {
	switch {
	case d == 0:
		return 0
	case d < time.Millisecond:
		return -1
	}
	return int(d / time.Millisecond)
}

func do(ctx context.Context, conn *peer.Conn, req request) (*Result, error) {
	if _, err := req.validate(); err != nil {
		return nil, err
	}
	s, err := conn.Open(ctx, ServiceName, req)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	// Resetting the stream also tells the host to stop (a long Type).
	stop := context.AfterFunc(ctx, s.Reset)
	defer stop()
	var rep reply
	if err := wire.ReadJSONLimit(s, &rep, 64<<10); err != nil {
		if ce := ctx.Err(); ce != nil {
			return nil, ce
		}
		return nil, fmt.Errorf("input: reading reply: %w", err)
	}
	if rep.Error != "" {
		return nil, errors.New(cli.Sanitize(rep.Error))
	}
	if rep.Result == nil {
		return nil, errors.New("input: host sent an empty reply")
	}
	r := rep.Result
	// Host-supplied text that callers print.
	r.Display.Name = cli.Sanitize(r.Display.Name)
	r.Warning = cli.Sanitize(r.Warning)
	if len(r.Keys) > MaxCombos {
		r.Keys = r.Keys[:MaxCombos]
	}
	for i := range r.Keys {
		r.Keys[i] = cli.Sanitize(r.Keys[i])
	}
	return r, nil
}

// plan is a validated request, ready for the host to run.
type plan struct {
	req    request
	button inject.Button
	combos []combo
	text   []rune
	delay  time.Duration
}

// validate checks a request (on the client before sending, and again on the
// host, which treats it as untrusted).
func (req request) validate() (*plan, error) {
	p := &plan{req: req}
	if s := req.ScaleFrom; s != nil && (s.Width <= 0 || s.Height <= 0 || s.Width > 1<<16 || s.Height > 1<<16) {
		return nil, fmt.Errorf("input: bad scale-from size %dx%d", s.Width, s.Height)
	}
	if len(req.Display) > 256 {
		return nil, errors.New("input: display name too long")
	}
	if req.DelayMS < -1 || req.DelayMS > int(MaxDelay/time.Millisecond) {
		return nil, fmt.Errorf("input: delay must be 0-%d ms", MaxDelay/time.Millisecond)
	}
	if req.DelayMS > 0 {
		p.delay = time.Duration(req.DelayMS) * time.Millisecond
	}
	needAt := false
	switch req.Op {
	case opClick:
		needAt = true
		switch req.Button {
		case "", "left":
			p.button, p.req.Button = inject.Left, "left"
		case "right":
			p.button = inject.Right
		case "middle":
			p.button = inject.Middle
		default:
			return nil, fmt.Errorf("input: unknown mouse button %q (want left, right or middle)", req.Button)
		}
		if req.Clicks < 1 || req.Clicks > 3 {
			return nil, errors.New("input: clicks must be 1-3")
		}
	case opMove:
		needAt = true
	case opScroll:
		if req.DX == 0 && req.DY == 0 {
			return nil, errors.New("input: scroll needs a non-zero DX or DY")
		}
		if abs(req.DX) > MaxScroll || abs(req.DY) > MaxScroll {
			return nil, fmt.Errorf("input: scroll at most %d notches per axis", MaxScroll)
		}
	case opType:
		if req.Text == "" {
			return nil, errors.New("input: nothing to type")
		}
		if len(req.Text) > MaxTextBytes || utf8.RuneCountInString(req.Text) > MaxTextRunes {
			return nil, fmt.Errorf("input: text longer than %d characters (%d KiB)", MaxTextRunes, MaxTextBytes>>10)
		}
		if !utf8.ValidString(req.Text) {
			return nil, errors.New("input: text is not valid UTF-8")
		}
		for i, r := range req.Text {
			if _, ok := inject.RuneKeysym(r); !ok {
				return nil, fmt.Errorf("input: cannot type character %U at byte %d (only printable text, newline and tab)", r, i)
			}
			// CR LF is one Return.
			if r == '\r' && i+1 < len(req.Text) && req.Text[i+1] == '\n' {
				continue
			}
			p.text = append(p.text, r)
		}
		if p.delay == 0 && req.DelayMS == 0 {
			p.delay = DefaultTypeDelay
		}
		if time.Duration(len(p.text))*(p.delay+time.Millisecond) > maxDuration {
			return nil, fmt.Errorf("input: typing this would take over %v; shorten the text or the delay", maxDuration)
		}
	case opKey:
		repeat := max(req.Repeat, 1)
		if req.Repeat < 0 || len(req.Keys) == 0 || len(req.Keys)*repeat > MaxCombos {
			return nil, fmt.Errorf("input: give 1-%d key combos in total (combos x repeat)", MaxCombos)
		}
		var once []combo
		for _, k := range req.Keys {
			c, err := parseCombo(k)
			if err != nil {
				return nil, fmt.Errorf("input: %w", err)
			}
			once = append(once, c)
		}
		for range repeat {
			p.combos = append(p.combos, once...)
		}
		if p.delay == 0 && req.DelayMS == 0 {
			p.delay = DefaultKeyDelay
		}
	default:
		return nil, fmt.Errorf("input: unknown operation %q", req.Op)
	}
	if needAt && req.At == nil {
		return nil, fmt.Errorf("input: %s needs coordinates", req.Op)
	}
	if a := req.At; a != nil && (a.X < 0 || a.Y < 0 || a.X >= 1<<16 || a.Y >= 1<<16) {
		return nil, fmt.Errorf("input: coordinates %d,%d are negative or implausible", a.X, a.Y)
	}
	return p, nil
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// toDisplay maps a point given in ScaleFrom pixels (or display pixels when
// scale is nil) to display pixels and to the virtual screen. A scaled
// pixel maps to the display pixel under its centre, so a downscaled
// screenshot's pixel lands inside the same on-screen feature.
func toDisplay(d screenshot.Display, scale *Size, p Point) (onDisp, virtual Point, err error) {
	w, h := d.Width, d.Height
	if scale != nil {
		w, h = scale.Width, scale.Height
	}
	if p.X < 0 || p.Y < 0 || p.X >= w || p.Y >= h {
		what := fmt.Sprintf("display %s (%dx%d)", displayLabel(d), d.Width, d.Height)
		if scale != nil {
			what = fmt.Sprintf("the %dx%d image given with --scale-from", w, h)
		}
		return Point{}, Point{}, fmt.Errorf("input: point %d,%d is outside %s; coordinates start at 0,0 top-left", p.X, p.Y, what)
	}
	onDisp = p
	if scale != nil {
		onDisp.X = min(d.Width-1, int((float64(p.X)+0.5)*float64(d.Width)/float64(w)))
		onDisp.Y = min(d.Height-1, int((float64(p.Y)+0.5)*float64(d.Height)/float64(h)))
	}
	return onDisp, Point{d.X + onDisp.X, d.Y + onDisp.Y}, nil
}

// fromVirtual maps a virtual-screen position back to display pixels and,
// with a scale, to ScaleFrom pixels.
func fromVirtual(d screenshot.Display, scale *Size, v Point) (onDisp Point, scaled *Point, inside bool) {
	onDisp = Point{v.X - d.X, v.Y - d.Y}
	inside = onDisp.X >= 0 && onDisp.Y >= 0 && onDisp.X < d.Width && onDisp.Y < d.Height
	if scale != nil && d.Width > 0 && d.Height > 0 {
		scaled = &Point{
			X: int((float64(onDisp.X) + 0.5) * float64(scale.Width) / float64(d.Width)),
			Y: int((float64(onDisp.Y) + 0.5) * float64(scale.Height) / float64(d.Height)),
		}
	}
	return onDisp, scaled, inside
}

func displayLabel(d screenshot.Display) string {
	if d.Index == screenshot.AllDisplays {
		return "all"
	}
	if d.Name != "" {
		return fmt.Sprintf("%d (%s)", d.Index, d.Name)
	}
	return strconv.Itoa(d.Index)
}
