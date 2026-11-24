package input

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jezek/xgb/xproto"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/inject"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/x11disp"
)

func TestPointerXvfb(t *testing.T) {
	x := startXvfb(t, 640, 480)
	rec := newRecorder(t, x)
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)

	// Left click: one press and one release of button 1 at the point.
	res, err := Click(ctx, client, 100, 50, ClickOptions{})
	if err != nil {
		t.Fatal(err)
	}
	evs := only(rec.take(count("brelease", 1)), "bpress", "brelease")
	if len(evs) != 2 || evs[0] != (ev{kind: "bpress", detail: 1, x: 100, y: 50}) || evs[1].kind != "brelease" || evs[1].detail != 1 {
		t.Fatalf("left click events %+v", evs)
	}
	if res.Action != "click" || res.Button != "left" || res.Clicks != 1 || *res.At != (Point{100, 50}) ||
		res.Pointer != (Point{100, 50}) || res.PointerVirtual != (Point{100, 50}) || !res.PointerOnDisplay ||
		res.Display.Width != 640 || res.Display.Index != 0 {
		t.Fatalf("result %+v", res)
	}

	// Right and middle buttons.
	if _, err := Click(ctx, client, 300, 200, ClickOptions{Button: "right"}); err != nil {
		t.Fatal(err)
	}
	evs = only(rec.take(count("brelease", 1)), "bpress")
	if len(evs) != 1 || evs[0].detail != 3 || evs[0].x != 300 || evs[0].y != 200 {
		t.Fatalf("right click %+v", evs)
	}
	if _, err := Click(ctx, client, 1, 2, ClickOptions{Button: "middle"}); err != nil {
		t.Fatal(err)
	}
	if evs = only(rec.take(count("brelease", 1)), "bpress"); evs[0].detail != 2 {
		t.Fatalf("middle click %+v", evs)
	}

	// Double click: two press/release pairs at the same point.
	if _, err := Click(ctx, client, 639, 479, ClickOptions{Double: true}); err != nil {
		t.Fatal(err)
	}
	evs = only(rec.take(count("brelease", 2)), "bpress", "brelease")
	var kinds []string
	for _, e := range evs {
		kinds = append(kinds, e.kind)
		if e.detail != 1 || e.x != 639 || e.y != 479 {
			t.Fatalf("double click event %+v", e)
		}
	}
	if strings.Join(kinds, ",") != "bpress,brelease,bpress,brelease" {
		t.Fatalf("double click %v", kinds)
	}

	// Move: motion to the point, no button.
	res, err = Move(ctx, client, 320, 240, MoveOptions{})
	if err != nil || res.Pointer != (Point{320, 240}) {
		t.Fatalf("move %+v %v", res, err)
	}
	evs = rec.take(func(evs []ev) bool { return len(evs) > 0 && evs[len(evs)-1] == (ev{kind: "motion", x: 320, y: 240}) })
	if len(only(evs, "bpress")) != 0 {
		t.Fatalf("move clicked: %+v", evs)
	}

	// Scroll: one wheel click per notch (5 down, 4 up, 7 right, 6 left),
	// at the given point.
	res, err = Scroll(ctx, client, -2, 3, ScrollOptions{At: &Point{50, 60}})
	if err != nil || res.DX != -2 || res.DY != 3 || *res.At != (Point{50, 60}) {
		t.Fatalf("scroll %+v %v", res, err)
	}
	evs = only(rec.take(count("brelease", 5)), "bpress")
	var wheel []byte
	for _, e := range evs {
		wheel = append(wheel, e.detail)
		if e.x != 50 || e.y != 60 {
			t.Fatalf("scroll position %+v", e)
		}
	}
	if !slices.Equal(wheel, []byte{5, 6, 5, 6, 5}) {
		t.Fatalf("wheel buttons %v", wheel)
	}
	if _, err := Scroll(ctx, client, 0, -1, ScrollOptions{}); err != nil {
		t.Fatal(err)
	}
	if evs = only(rec.take(count("brelease", 1)), "bpress"); evs[0].detail != 4 {
		t.Fatalf("scroll up %+v", evs)
	}

	// Off-screen coordinates are refused, not clamped.
	if _, err := Click(ctx, client, 640, 10, ClickOptions{}); err == nil || !strings.Contains(err.Error(), "outside display 0") {
		t.Fatalf("off-screen click: %v", err)
	}
	nothingHeld(t, x)
}

// keyEvents waits for n key releases and returns the key events as
// "+sym"/"-sym" strings (keysym names from the combo table or hex).
func keyEvents(rec *recorder, n int) []string {
	var out []string
	for _, e := range only(rec.take(count("release", n)), "press", "release") {
		sign := "+"
		if e.kind == "release" {
			sign = "-"
		}
		out = append(out, sign+symName(inject.Keysym(e.sym)))
	}
	return out
}

func symName(s inject.Keysym) string {
	for _, d := range keyDefs {
		if d.sym == s && len(d.name) > 1 {
			return d.name
		}
	}
	if s >= 0xffbe && s <= 0xffd5 {
		return "F" + string(rune('0'+(s-0xffbe+1)/10)) + string(rune('0'+(s-0xffbe+1)%10))
	}
	if r, ok := inject.KeysymRune(s); ok {
		return string(r)
	}
	return "?"
}

func TestKeyCombosXvfb(t *testing.T) {
	x := startXvfb(t, 320, 240)
	rec := newRecorder(t, x)
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)

	for _, tc := range []struct {
		combos []string
		want   string
	}{
		{[]string{"Return"}, "+Return -Return"},
		{[]string{"ctrl+c"}, "+ctrl +c -c -ctrl"},
		{[]string{"ctrl+shift+t"}, "+ctrl +shift +T -T -shift -ctrl"},
		{[]string{"shift+ctrl+T"}, "+shift +ctrl +T -T -ctrl -shift"},
		{[]string{"alt+F4"}, "+alt +F04 -F04 -alt"},
		{[]string{"super+l"}, "+super +l -l -super"},
		{[]string{"F1", "F12", "F24"}, "+F01 -F01 +F12 -F12 +F24 -F24"},
		{[]string{"Left", "Up", "Right", "Down"}, "+Left -Left +Up -Up +Right -Right +Down -Down"},
		{[]string{"Escape", "Tab", "BackSpace", "Delete"}, "+Escape -Escape +Tab -Tab +BackSpace -BackSpace +Delete -Delete"},
		{[]string{"Home", "End", "Page_Up", "Page_Down"}, "+Home -Home +End -End +Page_Up -Page_Up +Page_Down -Page_Down"},
		// "+" is on the shifted level of "=" on a US layout: Shift is added.
		{[]string{"ctrl+plus"}, "+ctrl +shift +plus -plus -shift -ctrl"},
	} {
		res, err := Key(ctx, client, tc.combos, KeyOptions{})
		if err != nil {
			t.Fatalf("%v: %v", tc.combos, err)
		}
		n := strings.Count(tc.want, "-")
		if got := strings.Join(keyEvents(rec, n), " "); got != tc.want {
			t.Errorf("%v: events %q, want %q", tc.combos, got, tc.want)
		}
		if len(res.Keys) != len(tc.combos) {
			t.Errorf("%v: result keys %v", tc.combos, res.Keys)
		}
		nothingHeld(t, x)
	}

	// --repeat presses the whole sequence again.
	res, err := Key(ctx, client, []string{"a", "b"}, KeyOptions{Repeat: 2})
	if err != nil || strings.Join(res.Keys, ",") != "a,b,a,b" {
		t.Fatalf("repeat %+v %v", res, err)
	}
	if got := strings.Join(keyEvents(rec, 4), " "); got != "+a -a +b -b +a -a +b -b" {
		t.Fatalf("repeat events %q", got)
	}

	// Unknown names are refused before anything is pressed, listing the
	// valid ones.
	if _, err := Key(ctx, client, []string{"ctrl+Foo"}, KeyOptions{}); err == nil ||
		!strings.Contains(err.Error(), `unknown key "Foo"`) || !strings.Contains(err.Error(), "Page_Up") {
		t.Fatalf("unknown key: %v", err)
	}
	nothingHeld(t, x)
}

func TestTypeXvfb(t *testing.T) {
	x := startXvfb(t, 320, 240)
	rec := newRecorder(t, x)
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)
	setup := xproto.Setup(x)
	before, err := xproto.GetKeyboardMapping(x, setup.MinKeycode, byte(setup.MaxKeycode-setup.MinKeycode+1)).Reply()
	if err != nil {
		t.Fatal(err)
	}

	// ASCII (with Shift for capitals and symbols), accented Latin, CJK,
	// an emoji beyond the BMP, newline and tab; CR LF is one Return.
	text := "Hi, A&z!~ çàéÑß — 世界 😀\r\n\tend"
	want := "Hi, A&z!~ çàéÑß — 世界 😀\n\tend"
	res, err := Type(ctx, client, text, TypeOptions{Delay: 2 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if res.Chars != len([]rune(want)) || res.Action != "type" {
		t.Fatalf("result %+v", res)
	}
	var got []rune
	for _, e := range only(rec.take(count("release", len([]rune(want)))), "press") {
		s := inject.Keysym(e.sym)
		if s == inject.KeysymShiftL || s == inject.KeysymShiftR {
			continue
		}
		r, ok := inject.KeysymRune(s)
		if !ok {
			t.Fatalf("typed keysym %#x", s)
		}
		got = append(got, r)
	}
	if string(got) != want {
		t.Fatalf("typed %q, want %q", string(got), want)
	}
	nothingHeld(t, x)

	// Spare keycodes bound for the unusual characters were given back.
	after, err := xproto.GetKeyboardMapping(x, setup.MinKeycode, byte(setup.MaxKeycode-setup.MinKeycode+1)).Reply()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before.Keysyms, after.Keysyms) {
		t.Fatal("keyboard mapping not restored after typing")
	}

	// Validation: control characters, empty text, too long.
	for _, bad := range []string{"", "a\x1bb", "\x7f", strings.Repeat("x", MaxTextRunes+1)} {
		if _, err := Type(ctx, client, bad, TypeOptions{}); err == nil {
			t.Errorf("typed %q", bad[:min(len(bad), 10)])
		}
	}
}

func TestSerialisedAndCancelledXvfb(t *testing.T) {
	x := startXvfb(t, 320, 240)
	rec := newRecorder(t, x)
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)

	// Two concurrent requests never interleave their characters.
	var wg sync.WaitGroup
	for _, s := range []string{"aaaaaaaaaa", "bbbbbbbbbb"} {
		wg.Go(func() {
			if _, err := Type(ctx, client, s, TypeOptions{Delay: 3 * time.Millisecond}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	var typed strings.Builder
	for _, e := range only(rec.take(count("release", 20)), "press") {
		r, _ := inject.KeysymRune(inject.Keysym(e.sym))
		typed.WriteRune(r)
	}
	if s := typed.String(); s != "aaaaaaaaaabbbbbbbbbb" && s != "bbbbbbbbbbaaaaaaaaaa" {
		t.Fatalf("interleaved: %q", s)
	}

	// Cancelling a long Type stops the host promptly and the next request
	// runs at once.
	cctx, cancel := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Type(cctx, client, strings.Repeat("c", 200), TypeOptions{Delay: 50 * time.Millisecond}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled type: %v", err)
	}
	if _, err := Key(ctx, client, []string{"Return"}, KeyOptions{}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("host kept typing after the cancel (%v)", d)
	}
	evs := only(rec.take(func(evs []ev) bool {
		return slices.ContainsFunc(evs, func(e ev) bool { return e.kind == "release" && e.sym == 0xff0d })
	}), "press")
	if n := len(evs) - 1; n < 1 || n > 12 {
		t.Fatalf("%d characters typed before the cancel took effect", n)
	}
	nothingHeld(t, x)
}

func TestMultiMonitorXvfb(t *testing.T) {
	x := startXvfb(t, 640, 480)
	splitMonitors(t, x)
	rec := newRecorder(t, x)
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)

	click := func(px, py int, tgt Target) *Result {
		t.Helper()
		res, err := Click(ctx, client, px, py, ClickOptions{Target: tgt})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	pressAt := func() (int16, int16) {
		t.Helper()
		e := only(rec.take(count("brelease", 1)), "bpress")[0]
		return e.x, e.y
	}

	// The primary is RIGHT (index 0, x 320): coordinates are relative to it.
	res := click(10, 20, Target{})
	if px, py := pressAt(); px != 330 || py != 20 {
		t.Fatalf("primary click landed at %d,%d", px, py)
	}
	if res.Display.Name != "RIGHT" || res.Display.X != 320 || *res.Virtual != (Point{330, 20}) || res.Pointer != (Point{10, 20}) {
		t.Fatalf("result %+v", res)
	}
	// By name and by index.
	click(10, 20, Target{Display: "LEFT"})
	if px, py := pressAt(); px != 10 || py != 20 {
		t.Fatalf("LEFT click landed at %d,%d", px, py)
	}
	res = click(319, 479, Target{Display: "1"})
	if px, py := pressAt(); px != 319 || py != 479 || res.Display.Name != "LEFT" {
		t.Fatalf("display 1 click landed at %d,%d (%+v)", px, py, res.Display)
	}
	// "all" is the whole virtual screen.
	click(500, 5, Target{Display: "all"})
	if px, py := pressAt(); px != 500 || py != 5 {
		t.Fatalf("all click landed at %d,%d", px, py)
	}
	// --scale-from: a 160x240 thumbnail of RIGHT (320x480); thumbnail pixel
	// (80,120) covers display pixels (160..161, 240..241), its centre maps
	// to (161,241).
	res = click(80, 120, Target{ScaleFrom: Size{160, 240}})
	if px, py := pressAt(); px != 320+161 || py != 241 {
		t.Fatalf("scaled click landed at %d,%d", px, py)
	}
	if *res.At != (Point{161, 241}) || res.PointerScaled == nil || *res.PointerScaled != (Point{80, 120}) {
		t.Fatalf("scaled result %+v", res)
	}
	// The pointer now sits on RIGHT: as seen from LEFT it is off-display.
	res, err := Key(ctx, client, []string{"Escape"}, KeyOptions{Target: Target{Display: "LEFT"}})
	if err != nil || res.PointerOnDisplay || res.Pointer != (Point{481, 241}) {
		t.Fatalf("pointer from LEFT %+v %v", res, err)
	}
	// Errors name the problem.
	for _, tc := range []struct {
		tgt  Target
		x, y int
		want string
	}{
		{Target{Display: "MIDDLE"}, 1, 1, `no display named "MIDDLE"`},
		{Target{Display: "7"}, 1, 1, "no display 7"},
		{Target{}, 320, 1, "outside display 0 (RIGHT) (320x480)"},
		{Target{ScaleFrom: Size{160, 240}}, 160, 1, "outside the 160x240 image"},
	} {
		if _, err := Click(ctx, client, tc.x, tc.y, ClickOptions{Target: tc.tgt}); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v, want %q", tc.tgt, err, tc.want)
		}
	}
}

func TestViewOnlyAndHeadless(t *testing.T) {
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)
	t.Setenv(inject.ViewOnlyEnv, "1")
	if _, err := Move(ctx, client, 1, 1, MoveOptions{}); err == nil || !strings.Contains(err.Error(), "input is disabled on this host") {
		t.Fatalf("view-only: %v", err)
	}
	t.Setenv(inject.ViewOnlyEnv, "0")

	// No X server reachable at all.
	restore := x11disp.SetSocketDir(t.TempDir())
	defer restore()
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	_, err := Click(ctx, client, 1, 1, ClickOptions{})
	if err == nil || !strings.Contains(err.Error(), "no display available on the host") || !strings.Contains(err.Error(), "X11") {
		t.Fatalf("headless: %v", err)
	}
}

func TestCLIXvfb(t *testing.T) {
	x := startXvfb(t, 400, 300)
	rec := newRecorder(t, x)
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)
	run := func(stdin string, args ...string) (int, string, string) {
		var so, se bytes.Buffer
		env := &cli.Env{Target: "test", Stdin: strings.NewReader(stdin), Stdout: &so, Stderr: &se,
			DialFunc: func(context.Context, *cli.Env) (*peer.Conn, error) { return client, nil }}
		code := cli.Lookup(args[0]).Run(ctx, env, args[1:])
		return code, so.String(), se.String()
	}

	// Flags after the coordinates, --json.
	code, so, se := run("", "click", "200", "100", "--right", "--double", "--json")
	if code != cli.ExitOK {
		t.Fatalf("click: %d %s", code, se)
	}
	var res Result
	if err := json.Unmarshal([]byte(so), &res); err != nil || res.Button != "right" || res.Clicks != 2 || res.Pointer != (Point{200, 100}) {
		t.Fatalf("click json %q %v", so, err)
	}
	if evs := only(rec.take(count("brelease", 2)), "bpress"); evs[0].detail != 3 || evs[1].detail != 3 {
		t.Fatalf("cli click %+v", evs)
	}

	// --scale-from and the human summary.
	code, so, se = run("", "move", "--scale-from", "200x150", "50", "75")
	if code != cli.ExitOK || !strings.Contains(so, "moved at 101,151 on display 0 (screen) (400x300); pointer at 101,151 (50,75 in the 200x150 image)") {
		t.Fatalf("move: %d %q %s", code, so, se)
	}

	// scroll with negative numbers and --at.
	code, so, se = run("", "scroll", "0", "-2", "--at", "10,10")
	if code != cli.ExitOK || !strings.Contains(so, "scrolled dx=0 dy=-2 at 10,10") {
		t.Fatalf("scroll: %d %q %s", code, so, se)
	}
	if evs := only(rec.take(count("brelease", 2)), "bpress"); evs[0].detail != 4 || evs[0].x != 10 {
		t.Fatalf("cli scroll %+v", evs)
	}

	// type from stdin, and key.
	code, so, se = run("ok\n", "type", "-", "--delay", "0")
	if code != cli.ExitOK || !strings.Contains(so, "typed 3 character(s)") {
		t.Fatalf("type: %d %q %s", code, so, se)
	}
	rec.take(count("release", 3))
	code, so, se = run("", "press", "ctrl+a", "Delete", "--json")
	if code != cli.ExitOK || !strings.Contains(so, `"keys":["ctrl+a","Delete"]`) {
		t.Fatalf("key: %d %q %s", code, so, se)
	}
	rec.take(count("release", 3))

	// Usage errors never reach the host.
	for _, args := range [][]string{
		{"click", "1"}, {"click", "a", "b"}, {"click", "1", "2", "--right", "--middle"}, {"click", "1", "2", "--scale-from", "10"},
		{"move", "-1", "5"}, {"type"}, {"type", "a", "b"}, {"type", "x", "--delay", "5000"}, {"press"}, {"press", "ctrl+nope"},
		{"press", "a+b"}, {"press", "--repeat", "0", "a"}, {"scroll", "1"}, {"scroll", "0", "0"}, {"scroll", "0", "1", "--at", "x"},
		{"scroll", "0", "500"}, {"click", "1", "2", "--bogus"},
	} {
		if code, _, se := run("", args...); code != cli.ExitUsage {
			t.Errorf("%v: exit %d (%s), want usage", args, code, se)
		}
	}
	code, _, se = run("", "press", "ctrl+nope")
	if !strings.Contains(se, `unknown key "nope"`) || !strings.Contains(se, "F1-F24") {
		t.Fatalf("key usage error %q", se)
	}
	// Host-side errors are ExitError.
	if code, _, se := run("", "click", "5000", "1"); code != cli.ExitError || !strings.Contains(se, "outside display 0") {
		t.Fatalf("off-screen: %d %s", code, se)
	}
	if code, _, _ := run("", "click", "--help"); code != cli.ExitOK {
		t.Fatalf("help exit %d", code)
	}
	nothingHeld(t, x)
}
