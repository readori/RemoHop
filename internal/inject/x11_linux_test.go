package inject

import (
	"bufio"
	"io"
	"log"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

func TestMain(m *testing.M) {
	// xgb complains about the missing ~/.Xauthority on every connection to
	// the test's private Xvfb.
	xgb.Logger = log.New(io.Discard, "", 0)
	os.Exit(m.Run())
}

func startXvfb(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("Xvfb"); err != nil {
		t.Skip("Xvfb not installed")
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("Xvfb", "-displayfd", "3", "-screen", "0", "320x240x24", "-nolisten", "tcp", "-noreset", "-ac")
	cmd.ExtraFiles = []*os.File{pw}
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	line := make(chan string, 1)
	go func() { s, _ := bufio.NewReader(pr).ReadString('\n'); line <- s; pr.Close() }()
	select {
	case n := <-line:
		return ":" + strings.TrimSpace(n)
	case <-time.After(15 * time.Second):
		t.Fatal("Xvfb did not start")
	}
	return ""
}

// typedRunes records the characters a focused window receives, translating
// each key press with the keymap fetched after the latest MappingNotify,
// as Xlib clients do.
func typedRunes(t *testing.T, display string) (get func() string) {
	t.Helper()
	c, err := xgb.NewConnDisplay(display)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	setup := xproto.Setup(c)
	scr := setup.DefaultScreen(c)
	var mu sync.Mutex
	var km *xproto.GetKeyboardMappingReply
	refresh := func() {
		r, err := xproto.GetKeyboardMapping(c, setup.MinKeycode, byte(setup.MaxKeycode-setup.MinKeycode+1)).Reply()
		if err == nil {
			mu.Lock()
			km = r
			mu.Unlock()
		}
	}
	refresh()
	wid, _ := xproto.NewWindowId(c)
	xproto.CreateWindow(c, 0, wid, scr.Root, 0, 0, 320, 240, 0, xproto.WindowClassInputOnly, 0,
		xproto.CwEventMask, []uint32{xproto.EventMaskKeyPress | xproto.EventMaskStructureNotify})
	xproto.MapWindow(c, wid)
	var typed []rune
	mapped := make(chan struct{})
	go func() {
		for {
			e, err := c.WaitForEvent()
			if e == nil && err == nil {
				return
			}
			switch e := e.(type) {
			case xproto.MapNotifyEvent:
				close(mapped)
			case xproto.MappingNotifyEvent:
				refresh()
			case xproto.KeyPressEvent:
				mu.Lock()
				per := int(km.KeysymsPerKeycode)
				row := km.Keysyms[(int(e.Detail)-int(setup.MinKeycode))*per:]
				sym := row[0]
				if e.State&xproto.ModMaskShift != 0 && row[1] != 0 {
					sym = row[1]
				}
				if r, ok := KeysymRune(Keysym(sym)); ok {
					typed = append(typed, r)
				}
				mu.Unlock()
			}
		}
	}()
	<-mapped
	xproto.SetInputFocusChecked(c, xproto.InputFocusPointerRoot, wid, xproto.TimeCurrentTime).Check()
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return string(typed)
	}
}

// With only two spare keycodes, typing more distinct unusual characters
// than that rebinds spares, each only after clients had time to use its
// previous binding, and every character still arrives right.
func TestSpareRotation(t *testing.T) {
	display := startXvfb(t)
	typed := typedRunes(t, display)
	c, err := xgb.NewConnDisplay(display)
	if err != nil {
		t.Fatal(err)
	}
	x, err := NewX11(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(x.spares) < 2 {
		t.Skipf("only %d spare keycodes", len(x.spares))
	}
	x.spares = x.spares[:2]
	before := slices.Clone(x.keymap)
	start := time.Now()
	const text = "aéèêAëē世é!"
	for _, r := range text {
		if err := x.TypeRune(r); err != nil {
			t.Fatal(err)
		}
	}
	// é and è take the two spares; ê, ë, ē, 世 and the final é each rebind
	// the least recently used one, waiting until it has been settled for
	// mapSettle. Waits overlap pairwise, so at least three periods pass.
	if d := time.Since(start); d < 3*mapSettle {
		t.Errorf("rebinding did not wait for clients (%v)", d)
	}
	if err := x.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for typed() != text && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := typed(); got != text {
		t.Fatalf("typed %q, want %q", got, text)
	}

	// The mapping is back to what it was.
	c2, err := xgb.NewConnDisplay(display)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	setup := xproto.Setup(c2)
	km, err := xproto.GetKeyboardMapping(c2, setup.MinKeycode, byte(setup.MaxKeycode-setup.MinKeycode+1)).Reply()
	if err != nil || !slices.Equal(km.Keysyms, before) {
		t.Fatalf("mapping not restored (%v)", err)
	}
}

func TestEvdevAndRelease(t *testing.T) {
	display := startXvfb(t)
	c, err := xgb.NewConnDisplay(display)
	if err != nil {
		t.Fatal(err)
	}
	x, err := NewX11(c)
	if err != nil {
		t.Fatal(err)
	}
	// Hold evdev KEY_LEFTSHIFT (42), Control by keysym and the left button,
	// then Close: nothing may stay down.
	if err := x.KeyEvdev(42, true); err != nil {
		t.Fatal(err)
	}
	if err := x.KeySym(KeysymControlL, true); err != nil {
		t.Fatal(err)
	}
	if err := x.Button(Left, true); err != nil {
		t.Fatal(err)
	}
	if err := x.KeyEvdev(9999, true); err == nil {
		t.Fatal("bogus evdev code accepted")
	}
	if err := x.Button(Button(42), true); err == nil {
		t.Fatal("bogus button accepted")
	}
	if err := x.TypeRune(0x1b); err == nil {
		t.Fatal("typed ESC as a character")
	}
	obs, err := xgb.NewConnDisplay(display)
	if err != nil {
		t.Fatal(err)
	}
	defer obs.Close()
	root := xproto.Setup(obs).DefaultScreen(obs).Root
	p, _ := xproto.QueryPointer(obs, root).Reply()
	if p.Mask&(xproto.KeyButMaskShift|xproto.KeyButMaskControl|xproto.KeyButMaskButton1) != xproto.KeyButMaskShift|xproto.KeyButMaskControl|xproto.KeyButMaskButton1 {
		t.Fatalf("held state not visible: %#x", p.Mask)
	}
	x.Close()
	p, _ = xproto.QueryPointer(obs, root).Reply()
	km, _ := xproto.QueryKeymap(obs).Reply()
	if p.Mask&0xff0f != 0 || slices.ContainsFunc(km.Keys, func(b byte) bool { return b != 0 }) {
		t.Fatalf("still held after Close: mask %#x keys %v", p.Mask, km.Keys)
	}
}
