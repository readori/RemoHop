package clip

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

func init() { peer.Handle(Service, handle) }

// oneShotTimeout bounds get/set exchanges (the largest item has to cross the
// link inside it).
const oneShotTimeout = 2 * time.Minute

func handle(ctx context.Context, conn *peer.Conn, s peer.Stream, raw json.RawMessage) error {
	var req request
	if err := peer.DecodeReq(raw, &req); err != nil {
		return wire.WriteJSON(s, message{Error: "bad request: " + err.Error()})
	}
	board := hostBoard(ctx)
	switch req.Op {
	case opInfo:
		return wire.WriteJSON(s, message{Backend: board.Name()})
	case opGet:
		s.SetDeadline(time.Now().Add(oneShotTimeout))
		c, err := board.Read(ctx)
		switch {
		case errors.Is(err, ErrEmpty):
			return wire.WriteJSON(s, message{Backend: board.Name(), Empty: true})
		case err != nil:
			return wire.WriteJSON(s, message{Backend: board.Name(), Error: err.Error()})
		}
		// Recorded before the reply, so the recording is complete by the time
		// the client has its answer (and never runs after the session).
		hostRecord(conn, DirPull, "get", c)
		return writeItem(s, message{Backend: board.Name()}, c, 0)
	case opSet:
		return handleSet(ctx, conn, s, board)
	case opHistory:
		// The host's history holds what every session, and the owner,
		// copied. A session on a grant gets the clipboard as it is now,
		// not that record.
		if g := grant.Of(conn); g != nil {
			return wire.WriteJSON(s, message{Error: (&grant.Refusal{Grant: g.Name(), What: "the clipboard history",
				Why: "it holds what other sessions and the host's owner copied; a grant gives the current clipboard only"}).Error()})
		}
		return handleHistory(ctx, s, board, req)
	case opSync:
		return handleSync(ctx, conn, s, board, req)
	default:
		return wire.WriteJSON(s, message{Error: "unknown clip operation " + req.Op})
	}
}

func handleSet(ctx context.Context, conn *peer.Conn, s peer.Stream, board Board) error {
	s.SetDeadline(time.Now().Add(oneShotTimeout))
	var m message
	if err := wire.ReadJSON(s, &m); err != nil {
		return err
	}
	if m.Item == nil {
		return wire.WriteJSON(s, message{Error: "set without an item"})
	}
	c, invalid, err := readItem(s, m.Item)
	if err != nil {
		return err
	}
	if invalid != nil {
		return wire.WriteJSON(s, message{Error: invalid.Error()})
	}
	if err := board.Write(ctx, c); err != nil {
		return wire.WriteJSON(s, message{Backend: board.Name(), Error: err.Error()})
	}
	hostRecord(conn, DirPush, "set", c) // before the reply, see opGet
	return wire.WriteJSON(s, message{Backend: board.Name()})
}

// hostRecord notes an item that crossed the host's clipboard in the host's
// own history, unless the host has turned that off.
func hostRecord(conn *peer.Conn, dir Direction, via string, c Content) {
	if !HostRecordingEnabled() {
		return
	}
	h, err := OpenHostHistory()
	if err != nil {
		slog.Warn("clip: cannot open host history", "err", err)
		return
	}
	if _, err := h.Add(Entry{Dir: dir, Via: via, Peer: conn.RemoteID()}, c); err != nil {
		slog.Warn("clip: recording host history failed", "err", err)
	}
}

// handleHistory serves the host's history to the client. Listings are
// bounded in count and carry only a text preview and no file paths; content
// is fetched one entry at a time through the normal item framing.
func handleHistory(ctx context.Context, s peer.Stream, board Board, req request) error {
	s.SetDeadline(time.Now().Add(oneShotTimeout))
	h, err := OpenHostHistory()
	if err != nil {
		return wire.WriteJSON(s, message{Error: "host history unavailable"})
	}
	limit := req.Limit
	if limit <= 0 || limit > MaxRemoteList {
		limit = MaxRemoteList
	}
	switch req.H {
	case histList:
		entries, err := h.List(Filter{Limit: limit})
		if err != nil {
			return wire.WriteJSON(s, message{Error: "host history unreadable"})
		}
		out := make([]Entry, len(entries))
		for i, e := range entries {
			out[i] = wireEntry(e)
		}
		return wire.WriteJSON(s, message{Backend: board.Name(), Entries: out})
	case histGet, histRestore:
		all, err := h.List(Filter{})
		if err != nil {
			return wire.WriteJSON(s, message{Error: "host history unreadable"})
		}
		for _, e := range all {
			if e.ID != req.ID || req.ID == "" {
				continue
			}
			c, err := e.Content()
			if err != nil {
				return wire.WriteJSON(s, message{Error: "history entry is not available in full: " + err.Error()})
			}
			we := wireEntry(e)
			if req.H == histRestore {
				if err := board.Write(ctx, c); err != nil {
					return wire.WriteJSON(s, message{Backend: board.Name(), Error: err.Error()})
				}
				return wire.WriteJSON(s, message{Backend: board.Name(), Entry: &we})
			}
			return writeItem(s, message{Backend: board.Name(), Entry: &we}, c, 0)
		}
		return wire.WriteJSON(s, message{Error: "no such history entry"})
	default:
		return wire.WriteJSON(s, message{Error: "unknown history action"})
	}
}

// wireEntry is e as sent to a client: metadata plus a short text preview,
// never a path on the host.
func wireEntry(e Entry) Entry {
	e.File = ""
	if len(e.Text) > previewBytes {
		e.Text = strings.ToValidUTF8(e.Text[:previewBytes], "")
		e.Truncated = true
	}
	return e
}

func handleSync(ctx context.Context, conn *peer.Conn, s peer.Stream, board Board, req request) error {
	e := &engine{
		board:  board,
		stream: s,
		role:   peer.RoleHost,
		send:   req.Pull, recv: req.Push, images: req.Images,
		dirOut: DirPull, dirIn: DirPush,
		report: func(t Transfer) {
			if t.Err != nil {
				slog.Debug("clip sync: item refused", "peer", conn.RemoteID(), "dir", t.Dir, "err", t.Err)
				return
			}
			slog.Debug("clip sync transfer", "peer", conn.RemoteID(), "dir", t.Dir, "kind", t.Kind, "bytes", t.Size)
		},
		commit: func(dir Direction, c Content) { hostRecord(conn, dir, "sync", c) },
	}
	e.afterWatch = func() error { return wire.WriteJSON(s, message{Backend: board.Name()}) }
	return e.run(ctx)
}
