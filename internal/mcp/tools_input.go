package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/svc/input"
	"github.com/readori/RemoHop/internal/svc/screenshot"
)

// view is what the last remohop_screenshot of a target showed: which display,
// which part of it, and how large the returned image was. The pointer tools
// use it to turn "a pixel in the image I was given" into a real screen
// position, which also works for region captures (a plain scale factor
// would not).
type view struct {
	display    int
	imgW, imgH int
	shows      rect
}

// toDisplay maps an image pixel to the display pixel at its centre.
func (v view) toDisplay(x, y int) (int, int, error) {
	if x < 0 || y < 0 || x >= v.imgW || y >= v.imgH {
		return 0, 0, fmt.Errorf("(%d,%d) is outside the last screenshot, which is %dx%d pixels; take a new remohop_screenshot and read the position from it", x, y, v.imgW, v.imgH)
	}
	return v.shows.X + (2*x+1)*v.shows.Width/(2*v.imgW), v.shows.Y + (2*y+1)*v.shows.Height/(2*v.imgH), nil
}

// views remembers the last screenshot per target.
type views struct {
	mu sync.Mutex
	m  map[string]view
}

func (vs *views) remember(target string, v view) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	if vs.m == nil {
		vs.m = map[string]view{}
	}
	vs.m[target] = v
}

func (vs *views) last(target string) (view, bool) {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	v, ok := vs.m[target]
	return v, ok
}

const (
	spaceScreenshot = "screenshot"
	spaceDisplay    = "display"
)

// pointerArgs are the coordinate arguments shared by the pointer tools.
type pointerArgs struct {
	CoordinateSpace string `json:"coordinate_space,omitempty" jsonschema:"What x,y mean. \"screenshot\" (default): pixel positions in the image most recently returned by remohop_screenshot for this target - just read them off the image. \"display\": real pixels of the display (origin top-left), see display_width/display_height."`
	Display         *int   `json:"display,omitempty" jsonschema:"Only with coordinate_space \"display\": monitor index from remohop_displays (default 0, the primary; -1 = the whole virtual screen). In \"screenshot\" space the display of that screenshot is used."`
}

// locate turns tool coordinates into a display position.
func (s *Server) locate(targetArg string, a pointerArgs, x, y int) (input.Target, int, int, error) {
	switch a.CoordinateSpace {
	case "", spaceScreenshot:
		if a.Display != nil {
			return input.Target{}, 0, 0, errors.New(`display can only be given with coordinate_space "display"; in "screenshot" space the coordinates belong to the display of the last screenshot`)
		}
		target, err := s.resolveTarget(targetArg)
		if err != nil {
			return input.Target{}, 0, 0, err
		}
		v, ok := s.views.last(target)
		if !ok {
			return input.Target{}, 0, 0, errors.New(`no screenshot of this machine has been taken in this session: call remohop_screenshot first and give x,y as pixel positions in that image (or pass coordinate_space "display" with real display pixels)`)
		}
		dx, dy, err := v.toDisplay(x, y)
		return input.Target{Display: displaySpec(v.display)}, dx, dy, err
	case spaceDisplay:
		d := 0
		if a.Display != nil {
			d = *a.Display
		}
		if d < screenshot.AllDisplays {
			return input.Target{}, 0, 0, errors.New("display must be -1 (whole virtual screen) or a monitor index from remohop_displays")
		}
		return input.Target{Display: displaySpec(d)}, x, y, nil
	}
	return input.Target{}, 0, 0, fmt.Errorf(`coordinate_space %q is not "screenshot" or "display"`, a.CoordinateSpace)
}

type clickIn struct {
	Target string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	X      int    `json:"x" jsonschema:"Horizontal position, by default in pixels of the last screenshot image (0 = left edge)."`
	Y      int    `json:"y" jsonschema:"Vertical position, by default in pixels of the last screenshot image (0 = top edge)."`
	Button string `json:"button,omitempty" jsonschema:"left (default), right or middle."`
	Double bool   `json:"double,omitempty" jsonschema:"Double-click."`
	pointerArgs
}

type moveIn struct {
	Target string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	X      int    `json:"x" jsonschema:"Horizontal position, by default in pixels of the last screenshot image."`
	Y      int    `json:"y" jsonschema:"Vertical position, by default in pixels of the last screenshot image."`
	pointerArgs
}

type scrollIn struct {
	Target string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	DY     int    `json:"dy,omitempty" jsonschema:"Wheel notches down (negative: up), at most 100 either way."`
	DX     int    `json:"dx,omitempty" jsonschema:"Wheel notches right (negative: left), at most 100 either way."`
	X      *int   `json:"x,omitempty" jsonschema:"Optional: move the pointer here first (scrolling goes to the window under the pointer). Give both x and y, by default in pixels of the last screenshot image."`
	Y      *int   `json:"y,omitempty" jsonschema:"See x."`
	pointerArgs
}

type typeIn struct {
	Target  string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	Text    string `json:"text" jsonschema:"Text to type into whatever has keyboard focus (max 10000 characters). Any Unicode; a newline presses Return, a tab presses Tab. Never a password or other secret."`
	DelayMS int    `json:"delay_ms,omitempty" jsonschema:"Pause after each character in milliseconds (default 12, max 1000); raise it for applications that drop fast input."`
}

type keyIn struct {
	Target string   `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	Keys   []string `json:"keys" jsonschema:"Key combos pressed in order, e.g. [\"ctrl+l\"], [\"Return\"], [\"alt+F4\"], [\"ctrl+a\", \"BackSpace\"]. Modifiers: ctrl, shift, alt, super. Named keys include Return, Tab, Escape, BackSpace, Delete, Home, End, Page_Up, Page_Down, Left, Right, Up, Down, space, F1-F12; single characters name themselves."`
	Repeat int      `json:"repeat,omitempty" jsonschema:"Press the whole sequence this many times (default 1)."`
}

// inputOut reports what the host did, in real display pixels.
type inputOut struct {
	Action        string   `json:"action"`
	Display       int      `json:"display" jsonschema:"Monitor index the positions refer to (-1 = whole virtual screen)."`
	DisplayWidth  int      `json:"display_width"`
	DisplayHeight int      `json:"display_height"`
	At            *point   `json:"at,omitempty" jsonschema:"Where the pointer action happened, in real display pixels."`
	Pointer       point    `json:"pointer" jsonschema:"Final pointer position in real display pixels."`
	Button        string   `json:"button,omitempty"`
	Clicks        int      `json:"clicks,omitempty"`
	Keys          []string `json:"keys,omitempty" jsonschema:"Combos pressed, canonical spelling."`
	Chars         int      `json:"chars,omitempty" jsonschema:"Characters typed."`
	DX            int      `json:"dx,omitempty"`
	DY            int      `json:"dy,omitempty"`
	Warning       string   `json:"warning,omitempty"`
}

type point struct {
	X int `json:"x"`
	Y int `json:"y"`
}

func toInputOut(r *input.Result) inputOut {
	o := inputOut{
		Action: r.Action, Display: r.Display.Index, DisplayWidth: r.Display.Width, DisplayHeight: r.Display.Height,
		Pointer: point(r.Pointer), Button: r.Button, Clicks: r.Clicks, Keys: r.Keys, Chars: r.Chars, DX: r.DX, DY: r.DY, Warning: r.Warning,
	}
	if r.At != nil {
		o.At = &point{r.At.X, r.At.Y}
	}
	cleanStrings(&o)
	return o
}

const verifyHint = " Take a remohop_screenshot to verify the result."

func (s *Server) registerInput() {
	const loop = "Work in a loop: remohop_screenshot to observe, one action, remohop_screenshot again to verify. "
	const coords = "By default x,y are pixel positions in the image most recently returned by remohop_screenshot for this machine (whole display or region; the server converts them to the real screen position), so read them straight off that image. "
	const person = "This drives the real desktop of the remote machine: a person there sees it happen and may be using the mouse and keyboard at the same time. "
	addTool(s, "remohop_click", "Click on the remote screen",
		"Move the remote mouse pointer to a position and click (left button by default; right, middle and double-click available). "+
			coords+loop+person+"Clicking can trigger anything a person could trigger with the mouse (closing windows, confirming dialogs, deleting), so look before you click. "+
			"Needs a graphical session on the remote machine and the host's \"input\" feature.",
		destructive(false), s.click)
	addTool(s, "remohop_move", "Move the remote mouse pointer",
		"Move the remote mouse pointer without clicking, e.g. to hover over an element for a tooltip or menu. "+coords+person,
		additive(), s.move)
	addTool(s, "remohop_scroll", "Scroll on the remote screen",
		"Turn the remote mouse wheel: dy notches down (negative up), dx notches right (negative left), optionally after moving the pointer to x,y so the right window scrolls. "+coords+loop,
		additive(), s.scroll)
	addTool(s, "remohop_type", "Type text on the remote keyboard",
		"Type text into whatever has keyboard focus on the remote machine, as if a person typed it (any Unicode text, independent of the keyboard layout). Click the field first and check with a screenshot that it has focus. "+
			"Use remohop_key for shortcuts and special keys. "+loop+person+
			"NEVER type passwords, tokens or other secrets: they would pass through this conversation and appear on a screen that can be captured. Ask the person to enter credentials themselves. "+
			"For entering commands prefer remohop_exec; use typing only for GUI applications.",
		destructive(false), s.typeText)
	addTool(s, "remohop_key", "Press keys on the remote keyboard",
		"Press key combos on the remote machine in sequence, e.g. \"Return\", \"ctrl+s\", \"alt+Tab\", \"ctrl+shift+t\". Modifiers are always released afterwards. "+
			"Shortcuts act on whatever has focus and can be destructive (ctrl+w, alt+F4, Delete); check the screen first. "+loop+person,
		destructive(false), s.key)
}

func (s *Server) click(ctx context.Context, in clickIn) (*reply[inputOut], error) {
	button := strings.ToLower(strings.TrimSpace(in.Button))
	switch button {
	case "", "left", "right", "middle":
	default:
		return nil, fmt.Errorf("button %q is not left, right or middle", in.Button)
	}
	tgt, x, y, err := s.locate(in.Target, in.pointerArgs, in.X, in.Y)
	if err != nil {
		return nil, err
	}
	return s.inputCall(ctx, in.Target, func(ctx context.Context, c *peer.Conn) (*input.Result, error) {
		return input.Click(ctx, c, x, y, input.ClickOptions{Target: tgt, Button: button, Double: in.Double})
	})
}

func (s *Server) move(ctx context.Context, in moveIn) (*reply[inputOut], error) {
	tgt, x, y, err := s.locate(in.Target, in.pointerArgs, in.X, in.Y)
	if err != nil {
		return nil, err
	}
	return s.inputCall(ctx, in.Target, func(ctx context.Context, c *peer.Conn) (*input.Result, error) {
		return input.Move(ctx, c, x, y, input.MoveOptions{Target: tgt})
	})
}

func (s *Server) scroll(ctx context.Context, in scrollIn) (*reply[inputOut], error) {
	if in.DX == 0 && in.DY == 0 {
		return nil, errors.New("give dy and/or dx (wheel notches; positive dy scrolls down)")
	}
	if abs(in.DX) > input.MaxScroll || abs(in.DY) > input.MaxScroll {
		return nil, fmt.Errorf("at most %d notches per call", input.MaxScroll)
	}
	if (in.X == nil) != (in.Y == nil) {
		return nil, errors.New("give both x and y, or neither")
	}
	opts := input.ScrollOptions{}
	if in.X != nil {
		tgt, x, y, err := s.locate(in.Target, in.pointerArgs, *in.X, *in.Y)
		if err != nil {
			return nil, err
		}
		opts.Target, opts.At = tgt, &input.Point{X: x, Y: y}
	}
	return s.inputCall(ctx, in.Target, func(ctx context.Context, c *peer.Conn) (*input.Result, error) {
		return input.Scroll(ctx, c, in.DX, in.DY, opts)
	})
}

func (s *Server) typeText(ctx context.Context, in typeIn) (*reply[inputOut], error) {
	if in.Text == "" {
		return nil, errors.New("text is empty")
	}
	if len(in.Text) > input.MaxTextBytes || len([]rune(in.Text)) > input.MaxTextRunes {
		return nil, fmt.Errorf("text is too long (max %d characters per call); split it", input.MaxTextRunes)
	}
	if in.DelayMS < 0 || time.Duration(in.DelayMS)*time.Millisecond > input.MaxDelay {
		return nil, errors.New("delay_ms must be 0-1000")
	}
	return s.inputCall(ctx, in.Target, func(ctx context.Context, c *peer.Conn) (*input.Result, error) {
		return input.Type(ctx, c, in.Text, input.TypeOptions{Delay: time.Duration(in.DelayMS) * time.Millisecond})
	})
}

func (s *Server) key(ctx context.Context, in keyIn) (*reply[inputOut], error) {
	if len(in.Keys) == 0 {
		return nil, errors.New(`keys is empty; give combos such as ["ctrl+s"] or ["Return"]`)
	}
	repeat := max(in.Repeat, 1)
	if len(in.Keys)*repeat > input.MaxCombos {
		return nil, fmt.Errorf("at most %d key presses per call (keys x repeat)", input.MaxCombos)
	}
	for _, k := range in.Keys {
		if _, err := input.ValidateCombo(k); err != nil {
			return nil, fmt.Errorf("%w (valid key names: %s)", err, input.KeyNames())
		}
	}
	return s.inputCall(ctx, in.Target, func(ctx context.Context, c *peer.Conn) (*input.Result, error) {
		return input.Key(ctx, c, in.Keys, input.KeyOptions{Repeat: repeat})
	})
}

// inputCall performs one input action. Actions are never repeated on a new
// session: a click or keystroke that may already have happened must not
// happen twice.
func (s *Server) inputCall(ctx context.Context, target string, fn func(context.Context, *peer.Conn) (*input.Result, error)) (*reply[inputOut], error) {
	var res *input.Result
	err := s.withConn(ctx, target, false, func(ctx context.Context, c *peer.Conn) (err error) {
		res, err = fn(ctx, c)
		return err
	})
	if err != nil {
		return nil, displayErr(err)
	}
	out := toInputOut(res)
	var b strings.Builder
	switch out.Action {
	case "click":
		fmt.Fprintf(&b, "clicked %s x%d at display pixel %d,%d", out.Button, out.Clicks, out.At.X, out.At.Y)
	case "move":
		fmt.Fprintf(&b, "pointer moved to display pixel %d,%d", out.Pointer.X, out.Pointer.Y)
	case "scroll":
		fmt.Fprintf(&b, "scrolled dx=%d dy=%d with the pointer at display pixel %d,%d", out.DX, out.DY, out.Pointer.X, out.Pointer.Y)
	case "type":
		fmt.Fprintf(&b, "typed %d characters", out.Chars)
	case "key":
		fmt.Fprintf(&b, "pressed %s", strings.Join(out.Keys, ", "))
	default:
		b.WriteString(out.Action + " done")
	}
	fmt.Fprintf(&b, " (display %d, %dx%d).", out.Display, out.DisplayWidth, out.DisplayHeight)
	if out.Warning != "" {
		b.WriteString(" Warning: " + out.Warning + ".")
	}
	b.WriteString(verifyHint)
	return textReply(out, b.String()), nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
