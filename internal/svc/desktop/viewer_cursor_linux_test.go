package desktop

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/jezek/xgb/xproto"
)

// pagePointer is window.remohop.cursor() of the viewer page.
type pagePointer struct {
	On      bool
	CSS     string
	State   *CursorState
	Decoded int
	Overlay *struct{ X, Y float64 }
	Warped  bool
}

// The viewer page draws the pointer itself (a CSS cursor, so it moves at
// the mouse's rate) and the picture does not show it; once the pointer is
// moved on the host, the page draws it where it is, until the mouse moves
// again, and while the mouse is outside the picture. An older page, which
// does not say it draws the pointer, gets it in the picture, and no CSS
// cursor over it.
func TestViewerCursorXvfb(t *testing.T) {
	x := startXvfb(t, 640, 480)
	paint(t, x, rect{0, 0, 640, 480, 0x3060a0})
	root := xproto.Setup(x).DefaultScreen(x).Root

	t.Run("page", func(t *testing.T) {
		// Once the page has put the pointer at 400,300, move it on the
		// host, as an application would.
		done := make(chan struct{})
		defer close(done)
		go func() {
			for {
				select {
				case <-done:
					return
				case <-time.After(50 * time.Millisecond):
				}
				p, err := xproto.QueryPointer(x, root).Reply()
				if err == nil && abs(int(p.RootX)-400) <= 3 && abs(int(p.RootY)-300) <= 3 {
					break
				}
			}
			select {
			case <-done:
				return
			case <-time.After(time.Second):
			}
			xproto.WarpPointer(x, 0, root, 0, 0, 0, 0, 100, 100)
			xproto.GetInputFocus(x).Reply()
		}()
		var res struct {
			First, Warped, Outside pagePointer
			Worst                  int
			Box                    struct{ X, Y, Width, Height float64 }
		}
		runViewerCheck(t, Options{FPS: 15}, "cursor", &res)
		t.Logf("css %.40s..., %d levels around the pointer, moved pointer drawn at %+v", res.First.CSS, res.Worst, res.Warped.Overlay)
		if !res.First.On || !strings.HasPrefix(res.First.CSS, "url(") || res.First.Decoded == 0 {
			t.Errorf("the page does not draw the pointer: %+v", res.First)
		}
		if res.Worst > 24 {
			t.Errorf("the picture shows the pointer: up to %d levels off the painted colour around it", res.Worst)
		}
		k := res.Box.Width / 640
		if o := res.Warped.Overlay; o == nil || math.Abs(o.X-(res.Box.X+100*k)) > 2 || math.Abs(o.Y-(res.Box.Y+100*k)) > 2 {
			t.Errorf("the moved pointer is drawn at %+v, want %.0f,%.0f", o, res.Box.X+100*k, res.Box.Y+100*k)
		}
		if res.Outside.Overlay == nil {
			t.Error("the pointer is not drawn while the mouse is outside the picture")
		}
	})

	t.Run("older page", func(t *testing.T) {
		var res struct {
			First pagePointer
			Worst int
		}
		runViewerCheck(t, Options{FPS: 15}, "cursor", &res, "NOCURSOR=1")
		t.Logf("css %q, %d levels around the pointer", res.First.CSS, res.Worst)
		if res.First.On || res.First.CSS != "none" {
			t.Errorf("an older page got the pointer apart: %+v", res.First)
		}
		if res.Worst < 60 {
			t.Errorf("the picture does not show the pointer (at most %d levels off the painted colour)", res.Worst)
		}
	})
}

// Mouse lock in the page: see the "plock" check of the browser script.
func TestViewerMouseLockXvfb(t *testing.T) {
	x := startXvfb(t, 640, 480)
	setRootCursor(t, x, -1) // a game hid the pointer
	var res struct {
		Suggestion, CSS string
		Locked, Auto    []struct {
			Type   string   `json:"type"`
			Code   string   `json:"code"`
			Button int      `json:"button"`
			Down   bool     `json:"down"`
			Rel    bool     `json:"rel"`
			X      *float64 `json:"x"`
			DX, DY float64
		}
		Req, First json.RawMessage
	}
	runViewerCheck(t, Options{FPS: 15}, "plock", &res, "PLOCK=1")
	t.Logf("suggestion %q, lock requests %s then %s, sent while locked %+v", res.Suggestion, res.First, res.Req, res.Locked)
	if res.CSS != "none" {
		t.Errorf("the hidden remote pointer shows as %q over the picture", res.CSS)
	}
	if !strings.Contains(string(res.First), `"unadjustedMovement":true`) {
		t.Errorf("the lock did not ask for raw movement: %s", res.First)
	}
	var got []string
	for _, m := range res.Locked {
		switch {
		case m.Type == "key" && m.Code == "KeyM":
			t.Error("the lock shortcut reached the remote machine")
		case m.Type == "move":
			t.Errorf("an absolute move while locked: %+v", m)
		case m.Type == "move_by":
			got = append(got, fmt.Sprintf("move_by %g,%g", m.DX, m.DY))
		case m.Type == "button":
			if !m.Rel || m.X != nil {
				t.Errorf("a button with a position while locked: %+v", m)
			}
			got = append(got, fmt.Sprintf("button %d %v", m.Button, m.Down))
		}
	}
	if s, want := strings.Join(got, ", "), "move_by 3,-2, button 0 true, button 0 false"; s != want {
		t.Errorf("sent while locked: %q, want %q", s, want)
	}
	if len(res.Auto) != 0 {
		t.Errorf("the click that locked the mouse was passed on: %+v", res.Auto)
	}
}
