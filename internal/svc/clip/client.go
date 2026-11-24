package clip

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

// Info describes the remote clipboard service.
type Info struct {
	// Backend names the host's clipboard backend: "x11/xclip", "wayland/...",
	// "macos", "windows/powershell", or "memory" for a headless host's
	// virtual clipboard.
	Backend string `json:"backend"`
}

// RemoteInfo asks the host which clipboard backend it uses.
func RemoteInfo(ctx context.Context, conn *peer.Conn) (Info, error) {
	s, err := conn.Open(ctx, Service, request{Op: opInfo})
	if err != nil {
		return Info{}, err
	}
	defer s.Close()
	var m message
	if err := wire.ReadJSON(s, &m); err != nil {
		return Info{}, err
	}
	return Info{Backend: meta(m.Backend)}, nil
}

// CallOptions tune Get/Set bookkeeping.
type CallOptions struct {
	// Peer is the name the item is recorded under in the history; empty
	// means conn.RemoteID(). Callers that know the name the user typed (an
	// alias) should pass it so history lookups by that name find the item.
	Peer string
	// NoHistory skips recording.
	NoHistory bool
	// Source is where the content originally came from, for the history
	// (Set only): empty means this machine. Re-sending something that was
	// pulled from another peer passes that peer's name.
	Source string
}

func (o CallOptions) peerName(conn *peer.Conn) string {
	if o.Peer != "" {
		return o.Peer
	}
	return conn.RemoteID()
}

// Get returns the remote clipboard, or ErrEmpty. The item is recorded in the
// local history under conn.RemoteID().
func Get(ctx context.Context, conn *peer.Conn) (Content, error) {
	return GetWith(ctx, conn, CallOptions{})
}

// GetWith is Get with explicit history options.
func GetWith(ctx context.Context, conn *peer.Conn, o CallOptions) (Content, error) {
	c, _, err := get(ctx, conn, o, "get")
	return c, err
}

func get(ctx context.Context, conn *peer.Conn, o CallOptions, via string) (Content, string, error) {
	s, err := conn.Open(ctx, Service, request{Op: opGet})
	if err != nil {
		return Content{}, "", err
	}
	defer s.Close()
	stop := deadline(ctx, s)
	defer stop()
	var m message
	if err := wire.ReadJSON(s, &m); err != nil {
		return Content{}, "", ctxErr(ctx, err)
	}
	m.Backend = meta(m.Backend)
	switch {
	case m.Error != "":
		return Content{}, m.Backend, errors.New("remote: " + m.Error)
	case m.Empty || m.Item == nil:
		return Content{}, m.Backend, ErrEmpty
	}
	c, invalid, err := readItem(s, m.Item)
	if err != nil {
		return Content{}, m.Backend, ctxErr(ctx, err)
	}
	if invalid != nil {
		return Content{}, m.Backend, invalid
	}
	if !o.NoHistory {
		record(nil, DirPull, via, o.peerName(conn), c)
	}
	return c, m.Backend, nil
}

// Set replaces the remote clipboard. The item is recorded in the local
// history under conn.RemoteID().
func Set(ctx context.Context, conn *peer.Conn, c Content) error {
	return SetWith(ctx, conn, c, CallOptions{})
}

// SetWith is Set with explicit history options.
func SetWith(ctx context.Context, conn *peer.Conn, c Content, o CallOptions) error {
	_, err := set(ctx, conn, o, "set", c)
	return err
}

func set(ctx context.Context, conn *peer.Conn, o CallOptions, via string, c Content) (backend string, err error) {
	c = normalize(c)
	if err := c.Validate(); err != nil {
		return "", err
	}
	s, err := conn.Open(ctx, Service, request{Op: opSet})
	if err != nil {
		return "", err
	}
	defer s.Close()
	stop := deadline(ctx, s)
	defer stop()
	if err := writeItem(s, message{}, c, 0); err != nil {
		return "", ctxErr(ctx, err)
	}
	var m message
	if err := wire.ReadJSON(s, &m); err != nil {
		return "", ctxErr(ctx, err)
	}
	m.Backend = meta(m.Backend)
	if m.Error != "" {
		return m.Backend, errors.New("remote: " + m.Error)
	}
	if !o.NoHistory {
		recordEntry(nil, Entry{Dir: DirPush, Via: via, Peer: o.peerName(conn), Source: o.Source}, c)
	}
	return m.Backend, nil
}

// deadline aborts a one-shot exchange when ctx ends.
func deadline(ctx context.Context, s peer.Stream) (stop func() bool) {
	s.SetDeadline(time.Now().Add(oneShotTimeout))
	return context.AfterFunc(ctx, s.Reset)
}

func ctxErr(ctx context.Context, err error) error {
	if ce := ctx.Err(); ce != nil {
		return ce
	}
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}

// SyncInfo is passed to SyncOptions.OnReady once the stream is established.
type SyncInfo struct {
	LocalBackend  string
	RemoteBackend string
}

// SyncOptions configures Sync. The zero value syncs text and images in both
// directions against the detected local clipboard.
type SyncOptions struct {
	// Local is the local clipboard; nil means Detect.
	Local Board
	// PushOnly sends local changes but ignores remote ones; PullOnly is the
	// reverse. Setting both is an error.
	PushOnly, PullOnly bool
	// NoImages restricts the sync to text.
	NoImages bool
	// OnReady is called once, after the handshake, before any transfer.
	OnReady func(SyncInfo)
	// OnTransfer is called for every item sent or received and for every
	// item that was refused (Transfer.Err).
	OnTransfer func(Transfer)
	// Peer names the remote in the history; default conn.RemoteID().
	Peer string
	// History receives every transferred item; nil means the default store
	// under the config directory. NoHistory disables recording.
	History   *History
	NoHistory bool
}

// Sync runs a bidirectional clipboard sync until ctx is cancelled or the
// session ends. It returns nil on cancellation and when the host closes the
// stream, and an error when the stream breaks.
//
// Only changes made after Sync starts are transferred. Conflicting changes
// made on both sides at once resolve to the later one (see the package
// documentation).
func Sync(ctx context.Context, conn *peer.Conn, opts SyncOptions) error {
	if opts.PushOnly && opts.PullOnly {
		return errors.New("clip: PushOnly and PullOnly are mutually exclusive")
	}
	local := opts.Local
	if local == nil {
		local = Detect(ctx)
	}
	peerName := opts.Peer
	if peerName == "" {
		peerName = conn.RemoteID()
	}
	s, err := conn.Open(ctx, Service, request{
		Op:     opSync,
		Push:   !opts.PullOnly,
		Pull:   !opts.PushOnly,
		Images: !opts.NoImages,
	})
	if err != nil {
		return err
	}
	defer s.Close()

	var hello message
	s.SetReadDeadline(time.Now().Add(15 * time.Second))
	if err := wire.ReadJSON(s, &hello); err != nil {
		return ctxErr(ctx, fmt.Errorf("clip sync handshake: %w", err))
	}
	s.SetReadDeadline(time.Time{})
	hello.Backend = meta(hello.Backend)
	if hello.Error != "" {
		return errors.New("remote: " + hello.Error)
	}

	hist := opts.History
	if hist == nil && !opts.NoHistory {
		hist = defaultHistory()
	}
	e := &engine{
		board:  local,
		stream: s,
		role:   peer.RoleClient,
		send:   !opts.PullOnly, recv: !opts.PushOnly, images: !opts.NoImages,
		dirOut: DirPush, dirIn: DirPull,
		report: func(t Transfer) {
			if t.Err == nil && hist != nil {
				record(hist, t.Dir, "sync", peerName, t.Content)
			}
			if opts.OnTransfer != nil {
				opts.OnTransfer(t)
			}
		},
	}
	e.afterWatch = func() error {
		if err := wire.WriteJSON(s, message{Backend: local.Name()}); err != nil {
			return err
		}
		if opts.OnReady != nil {
			opts.OnReady(SyncInfo{LocalBackend: local.Name(), RemoteBackend: hello.Backend})
		}
		return nil
	}
	if err := e.run(ctx); err != nil {
		return ctxErr(ctx, err)
	}
	return nil
}

func record(h *History, dir Direction, via, peerName string, c Content) {
	recordEntry(h, Entry{Dir: dir, Via: via, Peer: peerName}, c)
}

// recordEntry writes to the client-side history unless the user turned
// recording off (`remohop clip history off`).
func recordEntry(h *History, e Entry, c Content) {
	if !RecordingEnabled() {
		return
	}
	if h == nil {
		if h = defaultHistory(); h == nil {
			return
		}
	}
	if e.Source != "" && e.Dir == DirPush {
		e.SentTo = []string{e.Peer}
	}
	if _, err := h.Add(e, c); err != nil {
		slog.Warn("clip: recording history failed", "err", err)
	}
}

// sanitizeEntry makes a history entry received from the host safe to show:
// every string it carries is host-controlled.
func sanitizeEntry(e Entry) Entry {
	e.Peer, e.Via, e.Source, e.ID = meta(e.Peer), meta(e.Via), meta(e.Source), meta(e.ID)
	e.Hash, e.File = meta(e.Hash), ""
	if e.Kind != KindImage {
		e.Kind = KindText
	}
	for i := range e.SentTo {
		e.SentTo[i] = meta(e.SentTo[i])
	}
	if len(e.SentTo) > 16 {
		e.SentTo = e.SentTo[:16]
	}
	if len(e.Text) > previewBytes {
		e.Text = strings.ToValidUTF8(e.Text[:previewBytes], "")
		e.Truncated = true
	}
	return e
}

// RemoteHistory lists the host's own clipboard history: the newest limit
// entries (at most MaxRemoteList; 0 means that maximum), oldest first, with
// N numbering them from the newest. Text is a preview; use RemoteHistoryGet
// for the content.
func RemoteHistory(ctx context.Context, conn *peer.Conn, limit int) ([]Entry, error) {
	if limit <= 0 || limit > MaxRemoteList {
		limit = MaxRemoteList
	}
	s, err := conn.Open(ctx, Service, request{Op: opHistory, H: histList, Limit: limit})
	if err != nil {
		return nil, err
	}
	defer s.Close()
	stop := deadline(ctx, s)
	defer stop()
	var m message
	if err := wire.ReadJSON(s, &m); err != nil {
		return nil, ctxErr(ctx, err)
	}
	if m.Error != "" {
		return nil, errors.New("remote: " + meta(m.Error))
	}
	if len(m.Entries) > limit {
		m.Entries = m.Entries[len(m.Entries)-limit:]
	}
	// Number the entries ourselves: what the user picks must match what we
	// showed, whatever the host claims.
	for i := range m.Entries {
		m.Entries[i] = sanitizeEntry(m.Entries[i])
		m.Entries[i].N = len(m.Entries) - i
	}
	return m.Entries, nil
}

// RemoteHistoryGet fetches the content of the host history entry id.
func RemoteHistoryGet(ctx context.Context, conn *peer.Conn, id string) (Content, error) {
	s, err := conn.Open(ctx, Service, request{Op: opHistory, H: histGet, ID: id})
	if err != nil {
		return Content{}, err
	}
	defer s.Close()
	stop := deadline(ctx, s)
	defer stop()
	var m message
	if err := wire.ReadJSON(s, &m); err != nil {
		return Content{}, ctxErr(ctx, err)
	}
	if m.Error != "" || m.Item == nil {
		return Content{}, errors.New("remote: " + meta(m.Error))
	}
	c, invalid, err := readItem(s, m.Item)
	if err != nil {
		return Content{}, ctxErr(ctx, err)
	}
	if invalid != nil {
		return Content{}, invalid
	}
	return c, nil
}

// RemoteHistoryRestore makes the host put its history entry id back on its
// clipboard.
func RemoteHistoryRestore(ctx context.Context, conn *peer.Conn, id string) error {
	s, err := conn.Open(ctx, Service, request{Op: opHistory, H: histRestore, ID: id})
	if err != nil {
		return err
	}
	defer s.Close()
	stop := deadline(ctx, s)
	defer stop()
	var m message
	if err := wire.ReadJSON(s, &m); err != nil {
		return ctxErr(ctx, err)
	}
	if m.Error != "" {
		return errors.New("remote: " + meta(m.Error))
	}
	return nil
}
