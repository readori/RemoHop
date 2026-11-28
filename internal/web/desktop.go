package web

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"golang.org/x/net/websocket"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/svc/desktop"
)

// Desktop WebSocket protocol (the page-facing half of internal/svc/desktop's
// own browser viewer, re-implemented here because that package's viewer is
// unexported).
//
//	query: c, t, decode, view_only, fps, max_height, display, and cursor=1:
//	    the page draws the host's pointer itself (see below)
//	server -> page, text JSON:
//	    {"type":"hello", ...desktop.Hello, "view_only":bool}
//	    {"type":"cursor","shapes":[desktop.CursorShape],"state":desktop.CursorState}
//	    {"type":"stats","host":HostStats,"rtt_ms","local_drops"}
//	    {"type":"end","error":"...","refused":bool}
//	server -> page, binary: 'V' flags(1, bit0 key) seq(4) pts_us(8) capture_us(8)
//	    arrival_us(8) payload (big endian; capture and arrival on the gateway
//	    clock, which the page treats as its own)
//	page -> server, text JSON: ack{seq}, keyframe, config{fps,bitrate,max_height},
//	    display{display}, view_only{on}, and input: move{x,y}, button{button,down,x,y,rel},
//	    wheel{dx,dy}, key{code,down}, release_all, move_by{dx,dy} (a locked
//	    mouse; host pixels), and text{text}: type any Unicode text on the
//	    host (phones and input methods send this)
//
// The pointer. A page that asks with cursor=1 draws it (a CSS cursor that
// moves at the local mouse's rate): a host that can then leaves it out of
// the video, says so with hello.cursor and sends "cursor" messages. An
// older page does not ask and keeps the pointer in the video. Relative
// input (move_by, rel buttons) is input like the rest: dropped while the
// session is view-only, and refused by the session to a host without
// input or without hello.relative.

const frameVideoTag = 'V'

type deskCtl struct {
	Type      string  `json:"type"`
	Seq       uint32  `json:"seq"`
	FPS       int     `json:"fps"`
	Bitrate   int64   `json:"bitrate"`
	MaxHeight int     `json:"max_height"`
	Display   *int    `json:"display"`
	On        bool    `json:"on"`
	X         float64 `json:"x"`
	Y         float64 `json:"y"`
	Button    int     `json:"button"`
	Down      bool    `json:"down"`
	DX        float64 `json:"dx"`
	DY        float64 `json:"dy"`
	Code      string  `json:"code"`
	Text      string  `json:"text"`
	Rel       bool    `json:"rel"`
}

type deskHello struct {
	Type string `json:"type"`
	desktop.Hello
	ViewOnly bool `json:"view_only"`
}

// deskCursor is the host's pointer, for a page that draws it.
type deskCursor struct {
	Type   string                `json:"type"` // "cursor"
	Shapes []desktop.CursorShape `json:"shapes,omitempty"`
	State  desktop.CursorState   `json:"state"`
}

func (s *Server) handleDesktopWS(w http.ResponseWriter, r *http.Request) {
	ws := s.ticketAuth(w, r, "ws", true)
	if ws == nil {
		return
	}
	q := r.URL.Query()
	p, release, aerr := ws.acquire(q.Get("c"), s.now)
	if aerr != nil {
		writeErr(w, aerr)
		return
	}
	defer release()
	opts := desktop.Options{ManualAck: true, ViewOnly: q.Get("view_only") == "1", Cursor: q.Get("cursor") == "1"}
	opts.FPS, _ = strconv.Atoi(q.Get("fps"))
	opts.MaxHeight, _ = strconv.Atoi(q.Get("max_height"))
	opts.Display, _ = strconv.Atoi(q.Get("display"))
	if d := q.Get("decode"); d != "" {
		for _, c := range strings.Split(d, ",") {
			if c = strings.TrimSpace(c); c == "av1" || c == "h264" {
				opts.Decode = append(opts.Decode, c)
			}
		}
	}
	websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(c *websocket.Conn) {
			defer c.Close()
			c.MaxPayloadBytes = 64 << 10 // the page sends small JSON only
			s.bridgeDesktop(p.current(), &wsConn{ws: c}, opts)
		},
	}.ServeHTTP(w, r)
}

func (s *Server) bridgeDesktop(conn *peer.Conn, c *wsConn, opts desktop.Options) {
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	stopParent := context.AfterFunc(conn.Context(), cancel)
	defer stopParent()

	// The first message is the page's own: read until the session is open
	// would delay input, so the reader starts after Open.
	sess, err := desktop.Open(ctx, conn, opts)
	if err != nil {
		_, refused := errors.AsType[*peer.RemoteError](err)
		c.sendJSON(map[string]any{"type": "end", "error": classify(err).Message, "refused": refused})
		return
	}
	defer sess.Close()
	var viewOnly atomic.Bool
	viewOnly.Store(opts.ViewOnly)
	if c.sendJSON(deskHello{Type: "hello", Hello: sess.Hello(), ViewOnly: opts.ViewOnly}) != nil {
		return
	}

	go func() { // page -> host
		defer cancel()
		for {
			msg, err := c.recvText()
			if err != nil {
				return
			}
			var m deskCtl
			if json.Unmarshal([]byte(msg), &m) != nil {
				continue
			}
			switch m.Type {
			case "ack":
				sess.Ack(m.Seq)
			case "keyframe":
				sess.RequestKeyframe()
			case "behind":
				// The page's decoder cannot keep up: besides a key frame,
				// the host lowers the frame rate (it only asked for a key
				// frame before, so a slow decoder kept falling behind).
				sess.ReportBehind()
			case "config":
				sess.Configure(desktop.Config{FPS: m.FPS, Bitrate: m.Bitrate, MaxHeight: m.MaxHeight})
			case "display":
				if m.Display != nil && *m.Display >= desktop.DisplayAll && *m.Display <= 64 {
					sess.Configure(desktop.Config{Display: m.Display})
				}
			case "view_only":
				viewOnly.Store(m.On)
				// The session refuses input too (and lets go of what is
				// held), whatever reaches SendInput.
				sess.SetViewOnly(m.On)
			case "text":
				// Typed through the host's Unicode path, so accents, CJK input
				// and non-US host layouts work, unlike synthesised key codes.
				if viewOnly.Load() || m.Text == "" {
					continue
				}
				text := m.Text
				go func() {
					tctx, tcancel := context.WithTimeout(ctx, 30*time.Second)
					defer tcancel()
					if err := sess.TypeText(tctx, text); err != nil {
						s.log.Debug("web: desktop text input failed", "err", err)
					}
				}()
			case desktop.InputMove, desktop.InputButton, desktop.InputWheel, desktop.InputKey, desktop.InputReleaseAll,
				desktop.InputMoveBy:
				if viewOnly.Load() && m.Type != desktop.InputReleaseAll {
					continue
				}
				if len(m.Code) > 40 {
					continue
				}
				// The session itself refuses input to a host that takes
				// none, and relative input (move_by, rel buttons) to one
				// without hello.relative (desktop.ErrNoRelative).
				sess.SendInput(desktop.InputEvent{Kind: m.Type, X: m.X, Y: m.Y, Button: m.Button, Down: m.Down,
					DX: m.DX, DY: m.DY, Code: m.Code, Rel: m.Rel})
			}
		}
	}()

	for {
		ev, err := sess.Next(ctx)
		if err != nil {
			if conn.Context().Err() != nil || ctx.Err() == nil {
				reason := "the remote session ended"
				if e := sess.Err(); e != nil && conn.Context().Err() == nil {
					reason = cli.Sanitize(e.Error())
				}
				c.sendJSON(map[string]any{"type": "end", "error": reason})
			}
			return
		}
		switch {
		case ev.Hello != nil:
			err = c.sendJSON(deskHello{Type: "hello", Hello: *ev.Hello, ViewOnly: viewOnly.Load()})
		case ev.Cursor != nil:
			err = c.sendJSON(deskCursor{Type: "cursor", Shapes: ev.Cursor.Shapes, State: ev.Cursor.State})
		case ev.Stats != nil:
			err = c.sendJSON(map[string]any{"type": "stats", "host": ev.Stats,
				"rtt_ms": float64(sess.RTT().Microseconds()) / 1000, "local_drops": sess.LocalDrops()})
		case ev.Frame != nil:
			err = c.send(videoMessage(sess, ev.Frame))
		}
		if err != nil {
			return
		}
	}
}

const videoHeaderLen = 30

func videoMessage(sess *desktop.Session, f *desktop.Frame) []byte {
	msg := make([]byte, videoHeaderLen+len(f.Data))
	msg[0] = frameVideoTag
	if f.Key {
		msg[1] = 1
	}
	binary.BigEndian.PutUint32(msg[2:], f.Seq)
	binary.BigEndian.PutUint64(msg[6:], uint64(f.PTS.Microseconds()))
	var capture int64
	if !f.Capture.IsZero() {
		capture = sess.HostToLocal(f.Capture).UnixMicro()
	}
	binary.BigEndian.PutUint64(msg[14:], uint64(capture))
	binary.BigEndian.PutUint64(msg[22:], uint64(f.Received.UnixMicro()))
	copy(msg[videoHeaderLen:], f.Data)
	return msg
}
