package web

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/png"
	"io"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/svc/desktop"
	"github.com/readori/RemoHop/internal/wire"
)

// fakeDesktop plays a desktop host: it records the options each session
// asks for, sends hello (and the pointer, to a client that asked for it
// when hello.Cursor is set), and passes on the input it receives.
type fakeDesktop struct {
	hello desktop.Hello
	input chan desktop.Control

	mu   sync.Mutex
	opts []desktop.Options

	// audioErr, when set, is the host's refusal of audio; audioDone is
	// signalled each time an audio stream ends.
	audioErr  string
	audioDone chan struct{}
}

func newFakeDesktop(t *testing.T, hello desktop.Hello) (*fakeDesktop, *peer.Registry) {
	t.Helper()
	hello.Width, hello.Height, hello.Codec = 64, 64, "av01.0.04M.08"
	f := &fakeDesktop{hello: hello, input: make(chan desktop.Control, 64), audioDone: make(chan struct{}, 4)}
	reg := peer.HostServices.Wrapped(func(_ string, h peer.Handler) peer.Handler { return h })
	reg.Handle(desktop.ServiceName, f.serve)
	reg.Handle(desktop.AudioServiceName, f.serveAudio)
	return f, reg
}

func (f *fakeDesktop) serve(ctx context.Context, c *peer.Conn, s peer.Stream, req json.RawMessage) error {
	var o desktop.Options
	if err := peer.DecodeReq(req, &o); err != nil {
		return err
	}
	f.mu.Lock()
	f.opts = append(f.opts, o)
	f.mu.Unlock()
	body, _ := json.Marshal(f.hello)
	if err := wire.WriteFrame(s, 'H', body); err != nil {
		return err
	}
	if o.Cursor && f.hello.Cursor {
		var b bytes.Buffer
		png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 4, 6)))
		m, _ := json.Marshal(map[string]any{"id": 3, "x": 10, "y": 20, "visible": true,
			"shape": desktop.CursorShape{ID: 3, Width: 4, Height: 6, HotX: 1, HotY: 2, PNG: b.Bytes()}})
		if err := wire.WriteFrame(s, 'C', m); err != nil {
			return err
		}
	}
	for {
		var ctl desktop.Control
		if err := wire.ReadJSON(s, &ctl); err != nil {
			return nil
		}
		switch ctl.Type {
		case desktop.InputMove, desktop.InputMoveBy, desktop.InputButton, desktop.InputKey, desktop.InputWheel, desktop.InputReleaseAll:
			f.input <- ctl
		}
	}
}

// serveAudio plays the host's audio service: a hello and two packets,
// or the refusal in audioErr.
func (f *fakeDesktop) serveAudio(ctx context.Context, c *peer.Conn, s peer.Stream, req json.RawMessage) error {
	defer func() { f.audioDone <- struct{}{} }()
	f.mu.Lock()
	refusal := f.audioErr
	f.mu.Unlock()
	if refusal != "" {
		b, _ := json.Marshal(map[string]string{"error": refusal})
		return wire.WriteFrame(s, 'E', b)
	}
	b, _ := json.Marshal(desktop.AudioHello{Codec: "opus", SampleRate: 48000, Channels: 2, FrameMS: 20, Source: "test source"})
	if err := wire.WriteFrame(s, 'H', b); err != nil {
		return err
	}
	for seq := uint32(1); seq <= 2; seq++ {
		p := make([]byte, 12+3)
		binary.BigEndian.PutUint32(p, seq)
		binary.BigEndian.PutUint64(p[4:], uint64(time.Now().UnixMicro()))
		copy(p[12:], "opu")
		if err := wire.WriteFrame(s, 'A', p); err != nil {
			return err
		}
	}
	io.Copy(io.Discard, s) // until the client goes
	return nil
}

func (f *fakeDesktop) asked() []desktop.Options {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]desktop.Options(nil), f.opts...)
}

// next is the next input the host received; ok false if none came.
func (f *fakeDesktop) next(t *testing.T, wait time.Duration) (desktop.Control, bool) {
	t.Helper()
	select {
	case c := <-f.input:
		return c, true
	case <-time.After(wait):
		return desktop.Control{}, false
	}
}

func (f *fakeDesktop) want(t *testing.T, what string, ok func(desktop.Control) bool) {
	t.Helper()
	c, got := f.next(t, 10*time.Second)
	if !got || !ok(c) {
		t.Fatalf("%s: the host got %+v (%v)", what, c, got)
	}
}

// none checks that no input reached the host.
func (f *fakeDesktop) none(t *testing.T, what string) {
	t.Helper()
	if c, got := f.next(t, 400*time.Millisecond); got {
		t.Fatalf("%s: the host got %+v", what, c)
	}
}

func openDesk(t *testing.T, r *rig, params ...string) *websocket.Conn {
	t.Helper()
	ws := r.dialWS(t, "desktop/ws", r.q(append([]string{"decode", "av1"}, params...)...))
	t.Cleanup(func() { ws.Close() })
	return ws
}

// A page that draws the pointer asks for it and gets it (shape and state)
// apart from the video; relative input from a locked mouse reaches a
// host that takes it, and is dropped while the session is view only.
func TestDesktopBridgePointerAndRelative(t *testing.T) {
	f, reg := newFakeDesktop(t, desktop.Hello{Input: true, Relative: true, Cursor: true, ScreenWidth: 640, ScreenHeight: 480})
	r := newRigServing(t, peertest.Options{}, reg)
	ws := openDesk(t, r, "cursor", "1")
	h := readControl(t, ws, "hello")
	if h["cursor"] != true || h["relative"] != true || h["screen_width"] != 640.0 {
		t.Fatalf("hello %v", h)
	}
	if o := f.asked(); len(o) != 1 || !o[0].Cursor {
		t.Fatalf("the host was not asked for the pointer: %+v", o)
	}
	c := readControl(t, ws, "cursor")
	shapes, _ := c["shapes"].([]any)
	st, _ := c["state"].(map[string]any)
	if len(shapes) != 1 || st["id"] != 3.0 || st["x"] != 10.0 || st["y"] != 20.0 || st["visible"] != true {
		t.Fatalf("cursor %v", c)
	}
	if sh := shapes[0].(map[string]any); sh["hot_x"] != 1.0 || sh["hot_y"] != 2.0 || sh["png"] == "" {
		t.Fatalf("shape %v", sh)
	}

	sendTerm(t, ws, map[string]any{"type": "move_by", "dx": 3, "dy": -2})
	f.want(t, "move_by", func(c desktop.Control) bool { return c.Type == "move_by" && c.DX == 3 && c.DY == -2 })
	sendTerm(t, ws, map[string]any{"type": "button", "button": 0, "down": true, "rel": true})
	f.want(t, "rel button", func(c desktop.Control) bool { return c.Type == "button" && c.Rel && c.Down })

	// View only: what is held is let go, relative input goes nowhere.
	sendTerm(t, ws, map[string]any{"type": "view_only", "on": true})
	f.want(t, "view only", func(c desktop.Control) bool { return c.Type == "release_all" })
	sendTerm(t, ws, map[string]any{"type": "move_by", "dx": 5, "dy": 5})
	sendTerm(t, ws, map[string]any{"type": "button", "button": 0, "down": true, "rel": true})
	f.none(t, "relative input while view only")
	sendTerm(t, ws, map[string]any{"type": "view_only", "on": false})
	sendTerm(t, ws, map[string]any{"type": "move_by", "dx": 7, "dy": 0})
	f.want(t, "move_by after view only", func(c desktop.Control) bool { return c.Type == "move_by" && c.DX == 7 })
}

// An older page does not ask for the pointer: the host is not asked, and
// keeps it in the video.
func TestDesktopBridgeOldPage(t *testing.T) {
	f, reg := newFakeDesktop(t, desktop.Hello{Input: true, Relative: true})
	r := newRigServing(t, peertest.Options{}, reg)
	ws := openDesk(t, r)
	if h := readControl(t, ws, "hello"); h["cursor"] == true {
		t.Fatalf("hello %v", h)
	}
	if o := f.asked(); len(o) != 1 || o[0].Cursor {
		t.Fatalf("the host was asked for the pointer by a page that does not draw it: %+v", o)
	}
}

// Relative input needs what any input needs: a session opened view only,
// a host that takes no input, and a host without relative input get none.
func TestDesktopBridgeRelativeRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		hello  desktop.Hello
		params []string
	}{
		{"opened view only", desktop.Hello{Input: true, Relative: true}, []string{"view_only", "1"}},
		{"host input off", desktop.Hello{Input: false, Relative: true}, nil},
		{"host without relative input", desktop.Hello{Input: true}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, reg := newFakeDesktop(t, tc.hello)
			r := newRigServing(t, peertest.Options{}, reg)
			ws := openDesk(t, r, tc.params...)
			readControl(t, ws, "hello")
			sendTerm(t, ws, map[string]any{"type": "move_by", "dx": 3, "dy": 3})
			sendTerm(t, ws, map[string]any{"type": "button", "button": 0, "down": true, "rel": true})
			if tc.hello.Input && len(tc.params) == 0 {
				// Absolute input still goes: only relative is refused.
				sendTerm(t, ws, map[string]any{"type": "move", "x": 0.5, "y": 0.5})
				f.want(t, "absolute move", func(c desktop.Control) bool { return c.Type == "move" })
			}
			f.none(t, "relative input")
		})
	}
}

// The host's sound reaches the page when it asks: the stream's hello, then
// each packet as an 'A' message; asking it off ends the host's stream. A
// host that refuses says why.
func TestDesktopBridgeAudio(t *testing.T) {
	f, reg := newFakeDesktop(t, desktop.Hello{Input: true})
	r := newRigServing(t, peertest.Options{}, reg)
	ws := openDesk(t, r)
	readControl(t, ws, "hello")
	sendTerm(t, ws, map[string]any{"type": "audio", "on": true})
	m := readControl(t, ws, "audio")
	if a, _ := m["audio"].(map[string]any); m["on"] != true || a["codec"] != "opus" || a["source"] != "test source" {
		t.Fatalf("audio %v", m)
	}
	for n := 0; n < 2; {
		ws.SetReadDeadline(time.Now().Add(10 * time.Second))
		var msg []byte
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			t.Fatalf("waiting for audio packets: %v", err)
		}
		if len(msg) == 0 || msg[0] != 'A' {
			continue
		}
		n++
		if len(msg) != 21+3 || binary.BigEndian.Uint32(msg[1:]) != uint32(n) || string(msg[21:]) != "opu" ||
			binary.BigEndian.Uint64(msg[5:]) == 0 || binary.BigEndian.Uint64(msg[13:]) == 0 {
			t.Fatalf("audio packet %d: %x", n, msg)
		}
	}
	sendTerm(t, ws, map[string]any{"type": "audio", "on": false})
	select {
	case <-f.audioDone:
	case <-time.After(10 * time.Second):
		t.Fatal("the host's audio stream did not end when the page asked it off")
	}

	const off = "audio forwarding is switched off on this host"
	f.mu.Lock()
	f.audioErr = off
	f.mu.Unlock()
	sendTerm(t, ws, map[string]any{"type": "audio", "on": true})
	if m := readControl(t, ws, "audio"); m["on"] == true || m["error"] != off {
		t.Fatalf("refused audio: %v", m)
	}
}
