package input

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/cli"
)

// coordHelp is shared by every pointer command.
const coordHelp = `COORDINATES
  X Y are pixels of the selected display exactly as
      remohop <ID> screenshot -d DISPLAY
  shows it at FULL size, origin 0,0 at the top-left. Multiple monitors are
  handled for you (the host translates to its virtual screen; on macOS
  hosts to points, so Retina pixels work the same). "-d all" is not
  available on macOS hosts.
  If you looked at a DOWNSCALED screenshot (--max-width), either convert, or
  pass --scale-from WxH with that image's size and give coordinates in it:
      remohop <ID> screenshot --max-width 1280 -o s.png   # say s.png is 1280x720
      remohop <ID> click 640 360 --scale-from 1280x720     # centre of the display
  With screenshot -R X0,Y0,W,H, image pixel (x,y) is display pixel
  (X0+x, Y0+y).
`

const commonFlags = `  -d, --display D      monitor index or name from "screenshot --list"
                       (default the primary); "all" = whole virtual screen
      --scale-from WxH coordinates are in an image of this size showing the
                       whole display (see COORDINATES)
      --json           print the result as JSON: action, display (index,
                       name, x, y, width, height), at/virtual (where it
                       happened), pointer (final position, display pixels),
                       pointer_virtual, pointer_scaled, pointer_on_display
`

func init() {
	cli.Register(&cli.Command{
		Name:    "click",
		Summary: "click at a position on the remote screen",
		Remote:  true,
		Usage: `Usage: remohop <ID> click X Y [--right|--middle] [--double] [-d DISPLAY]
                      [--scale-from WxH] [--json]

Move the remote pointer to X,Y and click (left button unless told
otherwise). Observe with "screenshot" first, verify with another one after.

Flags:
      --right          click the right button
      --middle         click the middle button
      --double         double-click
` + commonFlags + `
` + coordHelp + `
Examples:
  remohop 123456789 click 412 230
  remohop 123456789 click 412 230 --double
  remohop 123456789 click 100 50 --right -d HDMI-1
  remohop 123456789 click 640 360 --scale-from 1280x720 --json
`,
		Run: runClick,
	})
	cli.Register(&cli.Command{
		Name:    "move",
		Summary: "move the remote mouse pointer",
		Remote:  true,
		Usage: `Usage: remohop <ID> move X Y [-d DISPLAY] [--scale-from WxH] [--json]

Move the remote pointer to X,Y without clicking (hover menus, tooltips).

Flags:
` + commonFlags + `
` + coordHelp + `
Examples:
  remohop 123456789 move 800 12
  remohop 123456789 move 400 300 --scale-from 1280x720 --json
`,
		Run: runMove,
	})
	cli.Register(&cli.Command{
		Name:    "type",
		Summary: "type text on the remote keyboard",
		Remote:  true,
		Usage: `Usage: remohop <ID> type [--delay MS] [--json] TEXT
       remohop <ID> type [--delay MS] [--json] -     (text from stdin)

Type TEXT into whatever has the keyboard focus on the remote machine (click
the field first). Any Unicode text works, whatever keyboard layout the host
uses: accents, CJK, emoji. A newline presses Return and a tab presses Tab,
so "line1\nline2" in a shell needs $'line1\nline2'. Text that starts with
"-" goes after "--": remohop <ID> type -- "-x". Text is limited to
` + strconv.Itoa(MaxTextRunes) + ` characters.

Never type passwords, tokens or other secrets this way: the text crosses
your agent's transcript and the remote machine's input history. Ask the
user to enter secrets themselves.

For shells and editors on the remote machine prefer "remohop <ID> shell -c" or
"remohop <ID> cp": typing into a terminal is slower and less reliable.

Flags:
      --delay MS       pause after each character (default ` + strconv.Itoa(int(DefaultTypeDelay/time.Millisecond)) + ` ms, max 1000;
                       raise it for slow applications)
  -d, --display D      display whose pointer position --json reports
      --json           print the result as JSON (action, chars, pointer, ...)

Examples:
  remohop 123456789 type "hello world"
  remohop 123456789 type $'first line\nsecond line\n'
  remohop 123456789 type "Grüße, 世界 👋"
  printf 'long text' | remohop 123456789 type -
`,
		Run: runType,
	})
	cli.Register(&cli.Command{
		// Not "key": that name is the local "remohop key" (identity key) and
		// the command registry is shared by local and remote commands.
		Name:    "press",
		Summary: "press keys or key combos on the remote keyboard",
		Remote:  true,
		Usage: `Usage: remohop <ID> press [--repeat N] [--delay MS] [--json] COMBO [COMBO...]

Press key combos in sequence. A combo is modifiers and one key joined by
"+": Return, ctrl+c, ctrl+shift+t, alt+F4, super+l, ctrl+plus. Modifiers
are pressed in the order given, then the key, and everything is released
in reverse order, also when something fails. Letters are taken in lower
case: ctrl+T is ctrl+t; add shift explicitly.

On macOS hosts super (alias cmd) is Command and alt is Option, so copy is
super+c; character keys are US key positions; F21-F24 and the media
play/next/previous keys do not exist there. Hosts need the Accessibility
permission (System Settings > Privacy & Security > Accessibility).

Key names (case-insensitive; "_" and "-" are optional):
  ` + KeyNames() + `

Flags:
      --repeat N       press the whole sequence N times (total combos <= ` + strconv.Itoa(MaxCombos) + `)
      --delay MS       pause between combos (default ` + strconv.Itoa(int(DefaultKeyDelay/time.Millisecond)) + ` ms)
  -d, --display D      display whose pointer position --json reports
      --json           print the result as JSON (action, keys, pointer, ...)

Examples:
  remohop 123456789 press Return
  remohop 123456789 press ctrl+a Delete
  remohop 123456789 press ctrl+shift+t
  remohop 123456789 press --repeat 5 Down
  remohop 123456789 press alt+F4
`,
		Run: runPress,
	})
	cli.Register(&cli.Command{
		Name:    "scroll",
		Summary: "turn the remote mouse wheel",
		Remote:  true,
		Usage: `Usage: remohop <ID> scroll DX DY [--at X,Y] [-d DISPLAY] [--scale-from WxH] [--json]

Turn the wheel DY notches down (negative: up) and DX notches right
(negative: left), at most ` + strconv.Itoa(MaxScroll) + ` per axis. Scrolling goes to the window under
the pointer: use --at to move there first.

Flags:
      --at X,Y         move the pointer to X,Y first (see COORDINATES)
` + commonFlags + `
` + coordHelp + `
Examples:
  remohop 123456789 scroll 0 5                 # five notches down
  remohop 123456789 scroll 0 -3 --at 600,400   # three up, over the given point
  remohop 123456789 scroll 4 0                 # right
`,
		Run: runScroll,
	})
}

// flags holds what the commands share.
type flags struct {
	fs        *flag.FlagSet
	display   string
	scaleFrom string
	asJSON    bool
}

func newFlags(env *cli.Env, name string, pointer bool) *flags {
	f := &flags{fs: cli.NewFlagSet(env, name, cli.Lookup(name).Usage)}
	f.fs.StringVar(&f.display, "display", "", "")
	f.fs.StringVar(&f.display, "d", "", "")
	f.fs.BoolVar(&f.asJSON, "json", false, "")
	if pointer {
		f.fs.StringVar(&f.scaleFrom, "scale-from", "", "")
	}
	return f
}

// parse parses flags anywhere among the arguments ("click 10 20 --right"
// as well as "click --right 10 20"). Negative numbers are arguments, not
// flags, so "scroll 0 -3" works; "--" ends flag parsing.
func (f *flags) parse(args []string) (pos []string, code int, ok bool) {
	for len(args) > 0 {
		a := args[0]
		switch {
		case a == "--":
			return append(pos, args[1:]...), 0, true
		case a == "-" || !strings.HasPrefix(a, "-") || isInt(a):
			pos = append(pos, a)
			args = args[1:]
			continue
		}
		// Hand the flag set everything up to the next negative number,
		// which it would otherwise take for an unknown flag, or "--".
		end := len(args)
		for i := 1; i < len(args); i++ {
			if args[i] == "--" || (isInt(args[i]) && strings.HasPrefix(args[i], "-")) {
				end = i
				break
			}
		}
		if err := f.fs.Parse(args[:end]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, cli.ExitOK, false
			}
			return nil, cli.ExitUsage, false
		}
		args = append(f.fs.Args(), args[end:]...)
	}
	return pos, 0, true
}

func isInt(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}

func (f *flags) target(env *cli.Env, name string) (Target, bool) {
	t := Target{Display: f.display}
	if f.scaleFrom != "" {
		s, err := ParseSize(f.scaleFrom)
		if err != nil {
			env.Errorf("%s: --scale-from: %v", name, err)
			return t, false
		}
		t.ScaleFrom = s
	}
	return t, true
}

func parseXY(env *cli.Env, name string, pos []string) (int, int, bool) {
	if len(pos) != 2 {
		env.Errorf("%s: want X Y (two integers), got %d argument(s); see `remohop help %s`", name, len(pos), name)
		return 0, 0, false
	}
	x, err1 := strconv.Atoi(pos[0])
	y, err2 := strconv.Atoi(pos[1])
	if err1 != nil || err2 != nil {
		env.Errorf("%s: X and Y must be integers, got %q %q", name, pos[0], pos[1])
		return 0, 0, false
	}
	return x, y, true
}

func parseDelay(env *cli.Env, name string, ms int, set bool) (time.Duration, bool) {
	if !set {
		return 0, true
	}
	if ms < 0 || ms > int(MaxDelay/time.Millisecond) {
		env.Errorf("%s: --delay must be 0-%d ms", name, MaxDelay/time.Millisecond)
		return 0, false
	}
	if ms == 0 {
		return time.Nanosecond, true // explicit zero, encoded as such by delayMS
	}
	return time.Duration(ms) * time.Millisecond, true
}

func flagSet(fs *flag.FlagSet, name string) bool {
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == name })
	return set
}

// execute validates req (a usage error if it is malformed), connects and
// runs it.
func execute(ctx context.Context, env *cli.Env, name string, f *flags, req request) int {
	if _, err := req.validate(); err != nil {
		env.Errorf("%s: %v", name, strings.TrimPrefix(err.Error(), "input: "))
		return cli.ExitUsage
	}
	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	res, err := do(ctx, conn, req)
	if err != nil {
		env.Errorf("%s: %v", name, strings.TrimPrefix(err.Error(), "input: "))
		return cli.ExitError
	}
	if f.asJSON {
		b, _ := json.Marshal(res)
		fmt.Fprintln(env.Stdout, string(b))
		return cli.ExitOK
	}
	fmt.Fprintln(env.Stdout, describe(res))
	return cli.ExitOK
}

// describe is the one-line human summary of a result.
func describe(r *Result) string {
	disp := "display " + displayLabel(r.Display)
	var b strings.Builder
	where := func() {
		if r.At != nil {
			fmt.Fprintf(&b, " at %d,%d", r.At.X, r.At.Y)
		}
	}
	switch r.Action {
	case opClick:
		b.WriteString("clicked " + r.Button)
		if r.Clicks == 2 {
			b.WriteString(" (double)")
		}
		where()
	case opMove:
		b.WriteString("moved")
		where()
	case opScroll:
		fmt.Fprintf(&b, "scrolled dx=%d dy=%d", r.DX, r.DY)
		where()
	case opType:
		fmt.Fprintf(&b, "typed %d character(s)", r.Chars)
	case opKey:
		b.WriteString("pressed " + strings.Join(r.Keys, ", "))
	default:
		b.WriteString(r.Action)
	}
	fmt.Fprintf(&b, " on %s (%dx%d); pointer at %d,%d", disp, r.Display.Width, r.Display.Height, r.Pointer.X, r.Pointer.Y)
	if r.PointerScaled != nil {
		fmt.Fprintf(&b, " (%d,%d in the %s image)", r.PointerScaled.X, r.PointerScaled.Y, r.ScaleFrom)
	}
	if !r.PointerOnDisplay {
		b.WriteString(", outside this display")
	}
	if r.Warning != "" {
		b.WriteString("; warning: " + r.Warning)
	}
	return b.String()
}

func runClick(ctx context.Context, env *cli.Env, args []string) int {
	f := newFlags(env, "click", true)
	var right, middle, double bool
	f.fs.BoolVar(&right, "right", false, "")
	f.fs.BoolVar(&middle, "middle", false, "")
	f.fs.BoolVar(&double, "double", false, "")
	pos, code, ok := f.parse(args)
	if !ok {
		return code
	}
	x, y, ok := parseXY(env, "click", pos)
	if !ok {
		return cli.ExitUsage
	}
	t, ok := f.target(env, "click")
	if !ok {
		return cli.ExitUsage
	}
	opts := ClickOptions{Target: t, Double: double}
	switch {
	case right && middle:
		env.Errorf("click: --right and --middle are exclusive")
		return cli.ExitUsage
	case right:
		opts.Button = "right"
	case middle:
		opts.Button = "middle"
	}
	return execute(ctx, env, "click", f, clickReq(x, y, opts))
}

func runMove(ctx context.Context, env *cli.Env, args []string) int {
	f := newFlags(env, "move", true)
	pos, code, ok := f.parse(args)
	if !ok {
		return code
	}
	x, y, ok := parseXY(env, "move", pos)
	if !ok {
		return cli.ExitUsage
	}
	t, ok := f.target(env, "move")
	if !ok {
		return cli.ExitUsage
	}
	return execute(ctx, env, "move", f, moveReq(x, y, MoveOptions{Target: t}))
}

func runType(ctx context.Context, env *cli.Env, args []string) int {
	f := newFlags(env, "type", false)
	var delay int
	f.fs.IntVar(&delay, "delay", 0, "")
	pos, code, ok := f.parse(args)
	if !ok {
		return code
	}
	if len(pos) != 1 {
		env.Errorf("type: want exactly one TEXT argument (quote it), or - for stdin; got %d", len(pos))
		return cli.ExitUsage
	}
	text := pos[0]
	if text == "-" {
		b, err := io.ReadAll(io.LimitReader(env.Stdin, MaxTextBytes+1))
		if err != nil {
			env.Errorf("type: reading stdin: %v", err)
			return cli.ExitError
		}
		text = string(b)
	}
	d, ok := parseDelay(env, "type", delay, flagSet(f.fs, "delay"))
	if !ok {
		return cli.ExitUsage
	}
	t, _ := f.target(env, "type")
	return execute(ctx, env, "type", f, typeReq(text, TypeOptions{Target: t, Delay: d}))
}

func runPress(ctx context.Context, env *cli.Env, args []string) int {
	f := newFlags(env, "press", false)
	var repeat, delay int
	f.fs.IntVar(&repeat, "repeat", 1, "")
	f.fs.IntVar(&delay, "delay", 0, "")
	pos, code, ok := f.parse(args)
	if !ok {
		return code
	}
	if len(pos) == 0 {
		env.Errorf("press: want at least one key combo, e.g. Return or ctrl+c")
		return cli.ExitUsage
	}
	if repeat < 1 {
		env.Errorf("press: --repeat must be at least 1")
		return cli.ExitUsage
	}
	d, ok := parseDelay(env, "press", delay, flagSet(f.fs, "delay"))
	if !ok {
		return cli.ExitUsage
	}
	t, _ := f.target(env, "press")
	return execute(ctx, env, "press", f, keyReq(pos, KeyOptions{Target: t, Repeat: repeat, Delay: d}))
}

func runScroll(ctx context.Context, env *cli.Env, args []string) int {
	f := newFlags(env, "scroll", true)
	var at string
	f.fs.StringVar(&at, "at", "", "")
	pos, code, ok := f.parse(args)
	if !ok {
		return code
	}
	if len(pos) != 2 {
		env.Errorf("scroll: want DX DY (notches; positive is right/down), got %d argument(s)", len(pos))
		return cli.ExitUsage
	}
	dx, err1 := strconv.Atoi(pos[0])
	dy, err2 := strconv.Atoi(pos[1])
	if err1 != nil || err2 != nil {
		env.Errorf("scroll: DX and DY must be integers, got %q %q", pos[0], pos[1])
		return cli.ExitUsage
	}
	t, ok := f.target(env, "scroll")
	if !ok {
		return cli.ExitUsage
	}
	opts := ScrollOptions{Target: t}
	if at != "" {
		xs, ys, found := strings.Cut(at, ",")
		x, e1 := strconv.Atoi(strings.TrimSpace(xs))
		y, e2 := strconv.Atoi(strings.TrimSpace(ys))
		if !found || e1 != nil || e2 != nil {
			env.Errorf("scroll: bad --at %q (want X,Y)", at)
			return cli.ExitUsage
		}
		opts.At = &Point{x, y}
	}
	return execute(ctx, env, "scroll", f, scrollReq(dx, dy, opts))
}
