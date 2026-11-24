package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"golang.org/x/net/websocket"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/svc/shell"
)

// Terminal WebSocket protocol.
//
//	server -> page, binary: raw terminal output (UTF-8, VT100/xterm escapes)
//	server -> page, text JSON: {"type":"attached","session","pid","created","keepalive_s"}
//	                           {"type":"exit","code","signal"}
//	                           {"type":"detached","reason"}
//	                           {"type":"refused","message"}   host refused the feature
//	                           {"type":"error","message"}
//	page -> server, text JSON: {"type":"in","data":"..."}  keystrokes or pasted text
//	                           {"type":"resize","rows","cols"}
//	                           {"type":"ping"}

const (
	maxTermMsg = 4 << 20 // a large paste
	maxDim     = 1000
)

type termMsg struct {
	Type string `json:"type"`
	Data string `json:"data"`
	Rows int    `json:"rows"`
	Cols int    `json:"cols"`
}

func (s *Server) handleTermWS(w http.ResponseWriter, r *http.Request) {
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
	rows, _ := strconv.Atoi(q.Get("rows"))
	cols, _ := strconv.Atoi(q.Get("cols"))
	rows, cols = clampDim(rows, 24), clampDim(cols, 80)
	req := shell.AttachRequest{Name: q.Get("session"), New: q.Get("new") == "1", Term: "xterm-256color", Rows: rows, Cols: cols}

	websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil }, // Origin was checked above
		Handler: func(c *websocket.Conn) {
			defer c.Close()
			c.MaxPayloadBytes = maxTermMsg
			s.bridgeTerm(r.Context(), p.conn, &wsConn{ws: c}, req)
		},
	}.ServeHTTP(w, r)
}

func clampDim(n, def int) int {
	if n <= 0 {
		return def
	}
	return min(n, maxDim)
}

func (s *Server) bridgeTerm(parent context.Context, conn *peer.Conn, c *wsConn, req shell.AttachRequest) {
	ctx, cancel := context.WithCancel(s.ctx)
	defer cancel()
	stopParent := context.AfterFunc(conn.Context(), cancel)
	defer stopParent()

	pr, pw := io.Pipe()
	defer pw.Close()
	resize := make(chan shell.WinSize, 1)

	go func() { // page -> host
		defer cancel()
		for {
			msg, err := c.recvText()
			if err != nil {
				return
			}
			var m termMsg
			if json.Unmarshal([]byte(msg), &m) != nil {
				continue
			}
			switch m.Type {
			case "in":
				if _, err := pw.Write([]byte(m.Data)); err != nil {
					return
				}
			case "resize":
				if m.Rows > 0 && m.Cols > 0 {
					ws := shell.WinSize{Rows: clampDim(m.Rows, 24), Cols: clampDim(m.Cols, 80)}
					select { // keep only the latest size
					case resize <- ws:
					default:
						select {
						case <-resize:
						default:
						}
						select {
						case resize <- ws:
						default:
						}
					}
				}
			}
		}
	}()

	res, err := shell.Attach(ctx, conn, req, shell.AttachIO{
		Stdin:  pr,
		Stdout: termWriter{c},
		Resize: resize,
		OnAttach: func(r *shell.AttachResult) {
			c.sendJSON(map[string]any{"type": "attached", "session": r.Session, "pid": r.PID,
				"created": r.Created, "keepalive_s": int(r.KeepAlive.Seconds())})
		},
	})
	switch {
	case err != nil:
		if _, ok := errors.AsType[*peer.RemoteError](err); ok {
			c.sendJSON(map[string]any{"type": "refused", "message": classify(err).Message})
		} else if ctx.Err() == nil {
			c.sendJSON(map[string]any{"type": "error", "message": cli.Sanitize(err.Error())})
		}
	case res.Exited:
		c.sendJSON(map[string]any{"type": "exit", "code": res.ExitCode, "signal": res.SignalName})
	case res.Detached != shell.DetachCanceled:
		c.sendJSON(map[string]any{"type": "detached", "reason": string(res.Detached)})
	}
}

type termWriter struct{ c *wsConn }

func (w termWriter) Write(p []byte) (int, error) {
	// websocket.Message.Send keeps a []byte until it is written; copy, as
	// the caller reuses p.
	if err := w.c.send(append([]byte(nil), p...)); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *Server) handleTermSessions(w http.ResponseWriter, r *http.Request, ws *webSession) {
	p, release, ok := s.conn(w, ws, r.URL.Query().Get("c"))
	if !ok {
		return
	}
	defer release()
	list, err := shell.Sessions(r.Context(), p.conn)
	if err != nil {
		writeErr(w, classify(err))
		return
	}
	if list == nil {
		list = []shell.SessionInfo{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": list})
}

func (s *Server) handleTermKill(w http.ResponseWriter, r *http.Request, ws *webSession) {
	var req struct {
		C    string `json:"c"`
		Name string `json:"name"`
	}
	if aerr := readJSON(w, r, &req, 1024); aerr != nil {
		writeErr(w, aerr)
		return
	}
	p, release, ok := s.conn(w, ws, req.C)
	if !ok {
		return
	}
	defer release()
	if err := shell.KillSession(r.Context(), p.conn, req.Name); err != nil {
		writeErr(w, classify(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
