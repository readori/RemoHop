package desktop

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jezek/xgb/xproto"
	"golang.org/x/net/websocket"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

func TestViewerBridgeXvfb(t *testing.T) {
	x := startXvfb(t, 640, 360)
	client, _ := peertest.Pair(t)
	v, err := newViewer(client, Options{FPS: 30}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go v.serve()
	defer v.close()

	ws := dialViewer(t, v)
	// The page says what it decodes first; a browser without AV1 gets
	// H.264.
	websocket.Message.Send(ws, `{"type":"caps","decode":["h264"]}`)
	var hello pageHello
	var text string
	if err := websocket.Message.Receive(ws, &text); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(text), &hello); err != nil || hello.Type != "hello" || hello.Width != 640 || hello.Height != 360 || !hello.Input || !hello.IsH264() {
		t.Fatalf("hello %q (%v)", text, err)
	}

	// Video arrives as binary messages; ack them as the page does. The
	// first one decoded must be a key frame.
	frames, keys := 0, 0
	deadline := time.Now().Add(10 * time.Second)
	for frames < 20 {
		if time.Now().After(deadline) {
			t.Fatalf("only %d frames", frames)
		}
		var msg []byte
		ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			t.Fatal(err)
		}
		if len(msg) == 0 || msg[0] != frameVideo {
			continue // a text message (stats/hello) decoded into bytes
		}
		hdr, err := parseFrameHeader(msg[1:])
		if err != nil {
			t.Fatal(err)
		}
		// Ack everything, as the page does once its decoder is idle.
		websocket.Message.Send(ws, `{"type":"ack","seq":`+itoaU(hdr.seq)+`}`)
		if frames == 0 && !hdr.key {
			continue // frames from before the page connected; a key follows
		}
		if hdr.key {
			keys++
		}
		if hdr.capture != 0 {
			if lat := time.Since(time.UnixMicro(hdr.capture)); lat < 0 || lat > 2*time.Second {
				t.Fatalf("implausible capture time (latency %v)", lat)
			}
		}
		frames++
	}
	if keys == 0 {
		t.Fatal("no key frame")
	}

	// Input sent by the page reaches the X server.
	websocket.Message.Send(ws, `{"type":"move","x":0.5,"y":0.5}`)
	root := xproto.Setup(x).DefaultScreen(x).Root
	waitPointer(t, x, root, 320, 180)

	// A second viewer takes over; the first is told why.
	ws2 := dialViewer(t, v)
	websocket.Message.Send(ws2, `{"type":"caps","decode":["av1","h264"]}`)
	for {
		ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		var m string
		if err := websocket.Message.Receive(ws, &m); err != nil {
			t.Fatalf("first viewer not notified: %v", err)
		}
		if strings.Contains(m, `"type":"end"`) {
			break
		}
	}
	ws2.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := websocket.Message.Receive(ws2, &text); err != nil || !strings.Contains(text, `"type":"hello"`) {
		t.Fatalf("second viewer hello: %q %v", text, err)
	}
}

func itoaU(v uint32) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// syncBuffer is a bytes.Buffer safe for the command goroutine and the test.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestDesktopCommandXvfb(t *testing.T) {
	startXvfb(t, 320, 240)
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	record := filepath.Join(t.TempDir(), "session.mp4")
	client, _ := peertest.Pair(t)
	var out, errb syncBuffer
	env := &cli.Env{Target: "test", Stdout: &out, Stderr: &errb,
		DialFunc: func(context.Context, *cli.Env) (*peer.Conn, error) { return client, nil }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	code := make(chan int, 1)
	go func() {
		code <- cli.Lookup("desktop").Run(ctx, env, []string{"--no-open", "--json", "--fps", "15", "--view-only",
			"--codec", "h264", "--record", record})
	}()
	var res desktopResult
	deadline := time.Now().Add(20 * time.Second)
	for {
		if json.Unmarshal([]byte(out.String()), &res) == nil && res.URL != "" {
			break
		}
		select {
		case c := <-code:
			t.Fatalf("exited %d: %s %s", c, out.String(), errb.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("no URL: %q %q", out.String(), errb.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if res.ScreenWidth != 320 || res.ScreenHeight != 240 || res.Input || res.Encoders["h264"] == "" || res.Record != record {
		t.Fatalf("result %+v", res)
	}
	resp, err := http.Get(res.URL)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("viewer page: %v %v", resp, err)
	}
	resp.Body.Close()

	// A page connects; what it receives is recorded.
	ws := dialURL(t, res.URL)
	websocket.Message.Send(ws, `{"type":"caps","decode":["av1","h264"]}`)
	frames := 0
	for frames < 30 {
		var msg []byte
		ws.SetReadDeadline(time.Now().Add(10 * time.Second))
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			t.Fatal(err)
		}
		if len(msg) > 0 && msg[0] == frameVideo {
			hdr, _ := parseFrameHeader(msg[1:])
			websocket.Message.Send(ws, `{"type":"ack","seq":`+itoaU(hdr.seq)+`}`)
			frames++
		}
	}
	cancel()
	select {
	case c := <-code:
		if c != cli.ExitOK {
			t.Fatalf("exit %d: %s", c, errb.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("command did not exit on cancel")
	}
	// The viewer is gone with the command.
	if _, err := http.Get(res.URL); err == nil {
		t.Fatal("viewer still serving after exit")
	}
	codec, w, h, n, start, _ := ffprobe(t, record)
	if codec != "h264" || w != 320 || h != 240 || n < 25 || n > 31 || start != 0 {
		t.Fatalf("recording: %s %dx%d, %d frames, starts at %v", codec, w, h, n, start)
	}
	if msg := decodeErrors(t, record); msg != "" {
		t.Fatalf("recording does not decode cleanly: %s", msg)
	}
}

// --record refuses to overwrite without --force, before connecting.
func TestDesktopCommandRecordExists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.mp4")
	os.WriteFile(path, []byte("keep"), 0o600)
	startXvfb(t, 320, 240)
	client, _ := peertest.Pair(t)
	var out, errb syncBuffer
	env := &cli.Env{Target: "test", Stdout: &out, Stderr: &errb,
		DialFunc: func(context.Context, *cli.Env) (*peer.Conn, error) { return client, nil }}
	if c := cli.Lookup("desktop").Run(context.Background(), env, []string{"--no-open", "--record", path}); c != cli.ExitError ||
		!strings.Contains(errb.String(), "already exists") {
		t.Fatalf("exit %d: %s", c, errb.String())
	}
	if b, _ := os.ReadFile(path); string(b) != "keep" {
		t.Fatal("existing file was touched")
	}
}

func TestDesktopCommandUsage(t *testing.T) {
	var out, errb syncBuffer
	env := &cli.Env{Target: "test", Stdout: &out, Stderr: &errb,
		DialFunc: func(context.Context, *cli.Env) (*peer.Conn, error) { t.Fatal("dialled"); return nil, nil }}
	for _, args := range [][]string{{"--fps", "0x"}, {"--bitrate", "lots"}, {"--scale", "3"}, {"extra"}, {"--port", "70000"}, {"--codec", "vp9"}, {"--record", "x.avi"}, {"--force"}} {
		if c := cli.Lookup("desktop").Run(context.Background(), env, args); c != cli.ExitUsage {
			t.Errorf("%v: exit %d", args, c)
		}
	}
}

// A page that cannot decode anything, or skips the capabilities, gets a
// clear end message instead of a stream.
func TestViewerCapsXvfb(t *testing.T) {
	startXvfb(t, 320, 240)
	client, _ := peertest.Pair(t)
	v, err := newViewer(client, Options{FPS: 15}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go v.serve()
	defer v.close()
	for _, tc := range []struct{ first, want string }{
		{`{"type":"caps","decode":[]}`, "neither AV1 nor H.264"},
		{`{"type":"caps","decode":["vp9"]}`, "neither AV1 nor H.264"},
		{`{"type":"move","x":1,"y":1}`, "capabilities first"},
	} {
		ws := dialViewer(t, v)
		websocket.Message.Send(ws, tc.first)
		var m string
		ws.SetReadDeadline(time.Now().Add(10 * time.Second))
		if err := websocket.Message.Receive(ws, &m); err != nil || !strings.Contains(m, `"type":"end"`) || !strings.Contains(m, tc.want) {
			t.Errorf("after %s: %q %v", tc.first, m, err)
		}
	}
}

// pageRequest sends a request the viewer answers and waits for the reply,
// acking video meanwhile as the page would.
func pageRequest(t *testing.T, ws *websocket.Conn, msg string, id uint32) pageReply {
	t.Helper()
	websocket.Message.Send(ws, msg)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var raw []byte
		ws.SetReadDeadline(time.Now().Add(10 * time.Second))
		if err := websocket.Message.Receive(ws, &raw); err != nil {
			t.Fatal(err)
		}
		if len(raw) > 0 && raw[0] == frameVideo {
			hdr, _ := parseFrameHeader(raw[1:])
			websocket.Message.Send(ws, `{"type":"ack","seq":`+itoaU(hdr.seq)+`}`)
			continue
		}
		var r pageReply
		if json.Unmarshal(raw, &r) == nil && r.Type == "reply" && r.ID == id {
			return r
		}
	}
	t.Fatalf("no reply to %s", msg)
	return pageReply{}
}

// The page's clipboard buttons go through the clip service, and its record
// button records on this machine.
func TestViewerPageRequestsXvfb(t *testing.T) {
	startXvfb(t, 320, 240)
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	client, _ := peertest.Pair(t)
	v, err := newViewer(client, Options{FPS: 15}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	v.recordDir, v.target = t.TempDir(), "123456789"
	go v.serve()
	defer v.close()
	ws := dialViewer(t, v)
	websocket.Message.Send(ws, `{"type":"caps","decode":["av1","h264"]}`)

	text := "from the viewer: héllo €"
	if r := pageRequest(t, ws, `{"type":"clip_send","id":1,"text":"`+text+`"}`, 1); r.Error != "" {
		t.Fatalf("clip_send: %s", r.Error)
	}
	if r := pageRequest(t, ws, `{"type":"clip_get","id":2}`, 2); r.Error != "" || r.Text != text {
		t.Fatalf("clip_get: %q %s", r.Text, r.Error)
	}

	r := pageRequest(t, ws, `{"type":"record","id":3,"on":true}`, 3)
	if r.Error != "" || len(r.Files) != 1 || !strings.HasPrefix(filepath.Base(r.Files[0]), "remohop-123456789-") {
		t.Fatalf("record on: %+v", r)
	}
	// Keep consuming as a page does (the host sends only what is acked).
	for end := time.Now().Add(10 * time.Second); v.recorder().Frames() < 10 && time.Now().Before(end); {
		var raw []byte
		ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		if err := websocket.Message.Receive(ws, &raw); err != nil {
			t.Fatal(err)
		}
		if len(raw) > 0 && raw[0] == frameVideo {
			hdr, _ := parseFrameHeader(raw[1:])
			websocket.Message.Send(ws, `{"type":"ack","seq":`+itoaU(hdr.seq)+`}`)
		}
	}
	stop := pageRequest(t, ws, `{"type":"record","id":4,"on":false}`, 4)
	if stop.Error != "" || len(stop.Files) != 1 || stop.Files[0] != r.Files[0] {
		t.Fatalf("record off: %+v", stop)
	}
	_, w, h, n, _, _ := ffprobe(t, stop.Files[0])
	if w != 320 || h != 240 || n < 5 {
		t.Fatalf("recording %dx%d with %d frames", w, h, n)
	}
}
