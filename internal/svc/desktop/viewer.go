package desktop

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/websocket"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/svc/clip"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

// The browser viewer: a loopback HTTP server serving one embedded page and
// a WebSocket that bridges the page to a desktop Session.
//
// Security model: anything able to talk to this server can drive the remote
// machine, so it binds to 127.0.0.1 only and every request must carry a
// 128-bit random token in its path. The Host header must name the loopback
// address (defeating DNS rebinding, where a hostile site's name resolves to
// 127.0.0.1) and WebSocket upgrades must come from the viewer's own origin
// (defeating cross-site WebSocket hijacking from any other page the user has
// open, which could otherwise connect to localhost ports freely).

//go:embed viewer.html
var viewerPage []byte

// PointerJS draws the host's pointer in a page and locks the mouse (see
// pointer.js): the viewer has it inlined, and the web console serves it.
// Read only.
//
//go:embed pointer.js
var PointerJS []byte

// AudioJS plays the host's audio in a page (see audio.js): the viewer
// has it inlined, and the web console serves it. Read only.
//
//go:embed audio.js
var AudioJS []byte

// viewerHTML is the page as served, with pointer.js and audio.js inlined
// (the page's Content-Security-Policy allows inline scripts only).
var viewerHTML = bytes.Replace(bytes.Replace(viewerPage, []byte("/*remohop:pointer.js*/"), PointerJS, 1),
	[]byte("/*remohop:audio.js*/"), AudioJS, 1)

type viewer struct {
	conn  *peer.Conn
	opts  Options
	token string
	port  int
	ln    net.Listener
	srv   *http.Server

	// recordDir is where recordings started from the page go; target
	// names the remote in their file names.
	recordDir string
	target    string

	// autoAudio (--audio) makes the page start audio on its own;
	// uploadDir is the host directory dropped files go to ("": Downloads).
	autoAudio bool
	uploadDir string
	fs        *rfs.Client // file service session for uploads, opened on first use

	mu       sync.Mutex
	active   chan struct{} // closed to evict the current viewer
	rec      *Recorder     // the recording in progress, if any
	quit     chan struct{} // closed when the viewer shuts down
	quitOnce sync.Once
}

// newViewer listens on addr (a loopback address). Sessions are opened when a
// page connects, so that the page can say first which codecs its browser
// decodes.
func newViewer(conn *peer.Conn, opts Options, addr string) (*viewer, error) {
	opts.ManualAck = true
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	tcp, ok := ln.Addr().(*net.TCPAddr)
	if !ok || !tcp.IP.IsLoopback() {
		ln.Close()
		return nil, fmt.Errorf("viewer must listen on a loopback address, not %s", ln.Addr())
	}
	var tok [16]byte
	rand.Read(tok[:])
	v := &viewer{
		conn: conn, opts: opts, port: tcp.Port, ln: ln, quit: make(chan struct{}),
		token: base64.RawURLEncoding.EncodeToString(tok[:]), recordDir: ".",
	}
	v.srv = &http.Server{Handler: v, ReadHeaderTimeout: 10 * time.Second}
	return v, nil
}

// startRecording records what the viewer receives into path (see
// Recorder), replacing any recording in progress.
func (v *viewer) startRecording(path string, force bool) (*Recorder, error) {
	rec, err := NewRecorder(path, force)
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	old := v.rec
	v.rec = rec
	v.mu.Unlock()
	if old != nil {
		old.Close()
	}
	return rec, nil
}

// stopRecording finishes the recording in progress and returns its files.
func (v *viewer) stopRecording() ([]string, error) {
	v.mu.Lock()
	rec := v.rec
	v.rec = nil
	v.mu.Unlock()
	if rec == nil {
		return nil, nil
	}
	err := rec.Close()
	return rec.Files(), err
}

func (v *viewer) recorder() *Recorder {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.rec
}

// recordName is the file a recording started from the page goes to.
func (v *viewer) recordName() string {
	name := "remohop"
	if t := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-' || r == '_' {
			return r
		}
		return -1
	}, v.target); t != "" {
		name += "-" + t
	}
	return filepath.Join(v.recordDir, name+"-"+time.Now().Format("20060102-150405")+".mp4")
}

// URL is what the user opens.
func (v *viewer) URL() string {
	return fmt.Sprintf("http://127.0.0.1:%d/%s/", v.port, v.token)
}

func (v *viewer) serve() error {
	err := v.srv.Serve(v.ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (v *viewer) close() {
	// Server.Close does not touch hijacked (WebSocket) connections; quit
	// ends the bridges.
	v.srv.Close()
	v.quitOnce.Do(func() { close(v.quit) })
	v.stopRecording()
	v.mu.Lock()
	fsc := v.fs
	v.fs = nil
	v.mu.Unlock()
	if fsc != nil {
		fsc.Close()
	}
}

func (v *viewer) allowedHost(h string) bool {
	p := strconv.Itoa(v.port)
	return h == "127.0.0.1:"+p || h == "localhost:"+p
}

func (v *viewer) allowedOrigin(o string) bool {
	p := strconv.Itoa(v.port)
	return o == "http://127.0.0.1:"+p || o == "http://localhost:"+p
}

func (v *viewer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !v.allowedHost(r.Host) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/")
	tok, sub, _ := strings.Cut(rest, "/")
	if !ok || subtle.ConstantTimeCompare([]byte(tok), []byte(v.token)) != 1 {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	switch sub {
	case "":
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		h.Set("Content-Type", "text/html; charset=utf-8")
		// img-src data: is for the pointer's CSS cursor images, which the
		// page makes itself (pointer.js); without it the browser shows its
		// default arrow instead.
		h.Set("Content-Security-Policy", fmt.Sprintf("default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; "+
			"connect-src 'self' ws://127.0.0.1:%d ws://localhost:%d; frame-ancestors 'none'; base-uri 'none'; form-action 'none'", v.port, v.port))
		w.Write(viewerHTML)
	case "ws":
		websocket.Server{
			Handshake: func(cfg *websocket.Config, r *http.Request) error {
				if !v.allowedOrigin(r.Header.Get("Origin")) {
					return errors.New("foreign origin")
				}
				return nil
			},
			Handler: v.bridge,
		}.ServeHTTP(w, r)
	case "upload":
		v.handleUpload(w, r)
	default:
		http.NotFound(w, r)
	}
}

// Text messages to the page.
type pageHello struct {
	Type string `json:"type"`
	Hello
	ViewOnly bool `json:"view_only"`
	// AudioAuto: the viewer was started with --audio, so the page should
	// ask for audio once it can play it.
	AudioAuto bool `json:"audio_auto,omitempty"`
}

func (v *viewer) pageHello(h Hello, viewOnly bool) pageHello {
	return pageHello{Type: "hello", Hello: h, ViewOnly: viewOnly, AudioAuto: v.autoAudio}
}

type pageStats struct {
	Type       string     `json:"type"`
	Host       *HostStats `json:"host"`
	RTTMS      float64    `json:"rtt_ms"`
	LocalDrops uint64     `json:"local_drops"`
}

// pageCursor is the host's pointer (Event.Cursor), for a page that draws
// it.
type pageCursor struct {
	Type   string        `json:"type"` // "cursor"
	Shapes []CursorShape `json:"shapes,omitempty"`
	State  CursorState   `json:"state"`
}

type pageEnd struct {
	Type  string `json:"type"`
	Error string `json:"error"`
}

// pageReply answers a page request that carried an id.
type pageReply struct {
	Type  string `json:"type"` // "reply"
	ID    uint32 `json:"id"`
	Error string `json:"error,omitempty"`
	// Text is the remote clipboard (clip_get); Files the recording files
	// (record).
	Text  string   `json:"text,omitempty"`
	Files []string `json:"files,omitempty"`
	// Audio describes the audio stream that was started (audio).
	Audio *AudioHello `json:"audio,omitempty"`
}

// pageRecording tells the page whether a recording runs.
type pageRecording struct {
	Type  string   `json:"type"` // "recording"
	On    bool     `json:"on"`
	Files []string `json:"files,omitempty"`
	Error string   `json:"error,omitempty"`
}

// pageMsg is a message from the page: a Control, the browser's decoder
// capabilities, or a request handled by the viewer itself.
type pageMsg struct {
	Control
	Decode []string `json:"decode,omitempty"` // caps
	// Cursor (caps): the page draws the pointer itself.
	Cursor bool `json:"cursor,omitempty"`
}

// Page message types handled by the viewer itself.
const (
	pageCaps     = "caps"      // first message: the codecs the browser decodes
	pageRecord   = "record"    // On: start or stop recording
	pageClipSend = "clip_send" // Text: set the remote clipboard
	pageClipGet  = "clip_get"  // fetch the remote clipboard
	pageAudio    = "audio"     // On: start or stop forwarding the host's audio
)

// maxClipText bounds clipboard text sent from the page.
const maxClipText = 1 << 20

func (v *viewer) bridge(ws *websocket.Conn) {
	defer ws.Close()
	ws.MaxPayloadBytes = maxClipText + 4096 // the page sends small JSON, or clipboard text

	// One viewer at a time: a new tab (or a reload) takes over.
	evict := make(chan struct{})
	v.mu.Lock()
	if v.active != nil {
		close(v.active)
	}
	v.active = evict
	v.mu.Unlock()
	defer func() {
		v.mu.Lock()
		if v.active == evict {
			v.active = nil
		}
		v.mu.Unlock()
	}()

	ctx, cancel := context.WithCancel(v.conn.Context())
	defer cancel()
	var wmu sync.Mutex // whole messages; Send also locks, but deadlines must pair with their message
	sendText := func(x any) error {
		b, _ := json.Marshal(x)
		wmu.Lock()
		defer wmu.Unlock()
		ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return websocket.Message.Send(ws, string(b))
	}

	// The page first says what its browser decodes; the host then picks
	// a codec within that.
	opts := v.opts
	ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	var first pageMsg
	var raw string
	if err := websocket.Message.Receive(ws, &raw); err != nil {
		return
	}
	ws.SetReadDeadline(time.Time{})
	if json.Unmarshal([]byte(raw), &first) != nil || first.Type != pageCaps {
		sendText(pageEnd{Type: "end", Error: "viewer protocol error: expected the decoder capabilities first"})
		return
	}
	opts.Decode = []string{}
	for _, c := range first.Decode {
		if validCodec(c) && !slices.Contains(opts.Decode, c) {
			opts.Decode = append(opts.Decode, c)
		}
	}
	if len(opts.Decode) == 0 {
		sendText(pageEnd{Type: "end", Error: "this browser can decode neither AV1 nor H.264 with WebCodecs; use a recent Chrome, Edge, Firefox or Safari"})
		return
	}
	// A page that draws the pointer gets it apart from the video, except
	// while recording: a recording shows the pointer only if the video
	// has it (see cursorFor).
	opts.Cursor = first.Cursor
	var curMu sync.Mutex
	cursorOn := opts.Cursor
	sess, err := Open(ctx, v.conn, opts)
	if err != nil {
		sendText(pageEnd{Type: "end", Error: err.Error()})
		return
	}
	defer sess.Close()
	// cursorFor keeps the pointer in the video while recording and out of
	// it otherwise, for a page that draws it.
	cursorFor := func(recording bool) {
		if !opts.Cursor {
			return
		}
		curMu.Lock()
		defer curMu.Unlock()
		if on := !recording; on != cursorOn {
			cursorOn = on
			sess.SetCursor(on)
		}
	}
	if v.recorder() != nil {
		cursorFor(true) // recording already (--record)
	}
	hello := sess.Hello()
	if rec := v.recorder(); rec != nil {
		rec.SetStream(hello)
	}
	if sendText(v.pageHello(hello, opts.ViewOnly)) != nil {
		return
	}
	if rec := v.recorder(); rec != nil {
		sendText(pageRecording{Type: "recording", On: true, Files: rec.Files()})
	}

	sendBin := func(msg []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return websocket.Message.Send(ws, msg)
	}
	ab := &audioBridge{v: v, ctx: ctx, sess: sess, sendBin: sendBin, sendText: sendText}
	defer ab.stop()

	// reply runs a request that waits on the host off the reader loop, so
	// input keeps flowing meanwhile, and answers it.
	reply := func(id uint32, f func() (pageReply, error)) {
		go func() {
			r, err := f()
			r.Type, r.ID = "reply", id
			if err != nil {
				r.Error = err.Error()
			}
			if id != 0 {
				sendText(r)
			}
		}()
	}
	call := func(f func(context.Context) error) func() (pageReply, error) {
		return func() (pageReply, error) {
			cctx, ccancel := context.WithTimeout(ctx, 30*time.Second)
			defer ccancel()
			return pageReply{}, f(cctx)
		}
	}

	go func() {
		defer cancel()
		for {
			var msg string
			if err := websocket.Message.Receive(ws, &msg); err != nil {
				return
			}
			var m pageMsg
			if json.Unmarshal([]byte(msg), &m) != nil {
				continue
			}
			c := m.Control
			switch {
			case c.Type == ctlAck:
				sess.Ack(c.Seq)
			case c.Type == ctlKeyframe:
				sess.RequestKeyframe()
			case c.Type == ctlBehind:
				sess.ReportBehind()
			case c.Type == ctlConfig && c.Display != nil:
				d := *c.Display
				reply(c.ID, call(func(ctx context.Context) error { return sess.SetDisplay(ctx, d) }))
			case c.Type == ctlConfig:
				sess.Configure(Config{FPS: c.FPS, Bitrate: c.Bitrate, MaxHeight: c.MaxHeight, Scale: c.Scale})
			case c.Type == ctlType:
				text := c.Text
				reply(c.ID, call(func(ctx context.Context) error { return sess.TypeText(ctx, text) }))
			case c.Type == ctlLock:
				reply(c.ID, call(sess.Lock))
			case c.Type == ctlBlockInput:
				on := c.On
				reply(c.ID, call(func(ctx context.Context) error { return sess.BlockInput(ctx, on) }))
			case c.Type == ctlBlackout:
				on := c.On
				reply(c.ID, call(func(ctx context.Context) error { return sess.Blackout(ctx, on) }))
			case c.Type == pageAudio:
				on := c.On
				reply(c.ID, func() (pageReply, error) { return ab.set(on) })
			case c.Type == "view_only":
				sess.SetViewOnly(c.On)
			case c.Type == pageRecord:
				on := c.On
				reply(c.ID, func() (pageReply, error) {
					if !on {
						files, err := v.stopRecording()
						cursorFor(false)
						sendText(pageRecording{Type: "recording", Files: files})
						return pageReply{Files: files}, err
					}
					cursorFor(true)
					rec, err := v.startRecording(v.recordName(), false)
					if err != nil {
						cursorFor(false)
						return pageReply{}, err
					}
					// Begin at the next key frame rather than wait up
					// to a GOP for one.
					rec.SetStream(sess.Hello())
					sess.RequestKeyframe()
					sendText(pageRecording{Type: "recording", On: true, Files: rec.Files()})
					return pageReply{Files: rec.Files()}, nil
				})
			case c.Type == pageClipSend:
				text := c.Text
				reply(c.ID, call(func(ctx context.Context) error {
					if len(text) > maxClipText {
						return fmt.Errorf("clipboard text is limited to %d bytes", maxClipText)
					}
					return clip.SetWith(ctx, v.conn, clip.Text(text), clip.CallOptions{Peer: v.target})
				}))
			case c.Type == pageClipGet:
				reply(c.ID, func() (pageReply, error) {
					cctx, ccancel := context.WithTimeout(ctx, 30*time.Second)
					defer ccancel()
					content, err := clip.GetWith(cctx, v.conn, clip.CallOptions{Peer: v.target})
					if err != nil {
						return pageReply{}, err
					}
					if content.Kind != clip.KindText {
						return pageReply{}, errors.New("the remote clipboard holds an image, not text")
					}
					return pageReply{Text: content.String()}, nil
				})
			case isInput(c.Type):
				sess.SendInput(c.input())
			}
		}
	}()
	go func() {
		select {
		case <-evict:
			sendText(pageEnd{Type: "end", Error: "another viewer took over this session"})
			cancel()
		case <-v.quit:
			sendText(pageEnd{Type: "end", Error: "remohop stopped"})
			cancel()
		case <-ctx.Done():
		}
	}()

	for {
		ev, err := sess.Next(ctx)
		if err != nil {
			if ctx.Err() == nil || v.conn.Context().Err() != nil {
				reason := "the remote session ended"
				if e := sess.Err(); e != nil && v.conn.Context().Err() == nil {
					reason = e.Error()
				}
				sendText(pageEnd{Type: "end", Error: reason})
			}
			return
		}
		switch {
		case ev.Hello != nil:
			if rec := v.recorder(); rec != nil {
				if rerr := rec.SetStream(*ev.Hello); rerr != nil {
					v.stopRecording()
					cursorFor(false)
					sendText(pageRecording{Type: "recording", Files: rec.Files(), Error: rerr.Error()})
				}
			}
			err = sendText(v.pageHello(*ev.Hello, opts.ViewOnly))
		case ev.Cursor != nil:
			err = sendText(pageCursor{Type: "cursor", Shapes: ev.Cursor.Shapes, State: ev.Cursor.State})
		case ev.Stats != nil:
			err = sendText(pageStats{Type: "stats", Host: ev.Stats,
				RTTMS: float64(sess.RTT().Microseconds()) / 1000, LocalDrops: sess.LocalDrops()})
		case ev.Frame != nil:
			if rec := v.recorder(); rec != nil {
				if rerr := rec.WriteFrame(ev.Frame); rerr != nil {
					v.stopRecording()
					cursorFor(false)
					sendText(pageRecording{Type: "recording", Files: rec.Files(), Error: rerr.Error()})
				}
			}
			wmu.Lock()
			err = v.sendFrame(ws, sess, ev.Frame)
			wmu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// sendFrame forwards a video frame as one binary message: 'V', the wire
// frame header with times translated to the local clock (so the page can
// compute glass-to-glass latency against Date.now()), then the temporal
// unit.
func (v *viewer) sendFrame(ws *websocket.Conn, sess *Session, f *Frame) error {
	msg := make([]byte, 1+frameHeaderLen+len(f.Data))
	msg[0] = frameVideo
	var capture int64
	if !f.Capture.IsZero() {
		capture = sess.HostToLocal(f.Capture).UnixMicro()
	}
	// The last header field carries the local arrival time instead of the
	// host send time.
	frameHeader{key: f.Key, seq: f.Seq, pts: f.PTS.Microseconds(), capture: capture,
		sent: f.Received.UnixMicro()}.put(msg[1:])
	copy(msg[1+frameHeaderLen:], f.Data)
	ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return websocket.Message.Send(ws, msg)
}
