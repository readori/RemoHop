package clip

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

// Direction of a transfer relative to the client machine.
type Direction string

const (
	// DirPush is client -> host.
	DirPush Direction = "push"
	// DirPull is host -> client.
	DirPull Direction = "pull"
)

// Transfer describes one clipboard item that moved during a sync, or one
// that was refused (Err set).
type Transfer struct {
	Dir     Direction
	Kind    Kind
	Size    int
	Hash    string
	Time    time.Time
	Content Content // empty when Err is set
	Err     error
}

// engine runs one side of a sync stream. The same code serves the client and
// the host; only the role (which breaks timestamp ties) and the direction
// switches differ.
type engine struct {
	board  Board
	stream peer.Stream
	role   peer.Role
	// send: forward local changes; recv: apply remote ones.
	send, recv, images bool
	// dirOut/dirIn name the direction of outgoing/incoming items.
	dirOut, dirIn Direction
	report        func(Transfer)
	// afterWatch runs once the board watch is established and before any
	// item is sent or applied; it sends the hello and tells the caller that
	// changes from now on are not missed.
	afterWatch func() error
	// commit, if set, runs synchronously just before an item takes effect
	// (before it is sent, or before it is written to the board). The host
	// records its history here: anything the peer can observe has then
	// already been recorded, so no recording can outlive the session.
	commit func(dir Direction, c Content)

	wmu sync.Mutex // serialises writes to the stream

	mu       sync.Mutex // guards the fields below and orders board writes
	last     string     // hash last sent or applied: the echo guard
	clock    int64      // Lamport clock
	haveSent bool
	sent     stamp
}

// maxLamport bounds counters received from the peer.
const maxLamport = 1 << 60

type stamp struct {
	ts   int64
	rank int // host outranks client on an exact tie
}

func (a stamp) less(b stamp) bool { return a.ts < b.ts || (a.ts == b.ts && a.rank < b.rank) }

func (e *engine) rank() int {
	if e.role == peer.RoleHost {
		return 1
	}
	return 0
}

func (e *engine) emit(t Transfer) {
	if e.report != nil {
		t.Time = time.Now()
		e.report(t)
	}
}

// run blocks until ctx ends, the peer closes the stream or the stream
// breaks. A cancelled context or a closing peer is a clean end (nil).
func (e *engine) run(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	// Unblocks the reader and flushes pending writes when we are done.
	stop := context.AfterFunc(ctx, func() { e.stream.Close() })
	defer stop()

	changes := e.board.Watch(ctx)
	if e.afterWatch != nil {
		if err := e.afterWatch(); err != nil {
			e.stream.Close()
			return err
		}
	}
	errc := make(chan error, 2)
	go func() { errc <- e.readLoop(ctx) }()
	go func() { errc <- e.watchLoop(ctx, changes) }()
	err := <-errc
	cancel()
	<-errc
	e.stream.Close()
	if err == io.EOF || parent.Err() != nil {
		return nil
	}
	return err
}

func (e *engine) watchLoop(ctx context.Context, changes <-chan Content) error {
	for {
		var c Content
		select {
		case <-ctx.Done():
			return nil
		case v, ok := <-changes:
			if !ok {
				return nil
			}
			c = v
		}
		if !e.send || (c.Kind == KindImage && !e.images) {
			continue
		}
		c = normalize(c)
		if err := c.Validate(); err != nil {
			e.emit(Transfer{Dir: e.dirOut, Kind: c.Kind, Size: len(c.Data), Err: err})
			continue
		}
		h := c.Hash()
		e.mu.Lock()
		if h == e.last {
			e.mu.Unlock()
			continue
		}
		// Watch events can arrive late. If a remote item was applied after
		// this change happened, the clipboard no longer holds it, and sending
		// it now would resurrect stale content with a fresh, higher stamp and
		// overwrite the peer. The check runs under the same lock as apply, so
		// an apply cannot slip in between the check and the stamp.
		if cur, err := e.board.Read(ctx); err != nil || cur.Hash() != h {
			e.mu.Unlock()
			continue
		}
		e.last = h
		e.clock++
		ts := e.clock
		e.sent, e.haveSent = stamp{ts, e.rank()}, true
		e.mu.Unlock()

		if e.commit != nil {
			e.commit(e.dirOut, c)
		}
		e.wmu.Lock()
		err := writeItem(e.stream, message{}, c, ts)
		e.wmu.Unlock()
		if err != nil {
			return err
		}
		e.emit(Transfer{Dir: e.dirOut, Kind: c.Kind, Size: len(c.Data), Hash: h, Content: c})
	}
}

func (e *engine) readLoop(ctx context.Context) error {
	for {
		var m message
		if err := wire.ReadJSON(e.stream, &m); err != nil {
			return err
		}
		if m.Item == nil {
			continue
		}
		c, invalid, err := readItem(e.stream, m.Item)
		if err != nil {
			return err
		}
		if invalid != nil {
			e.emit(Transfer{Dir: e.dirIn, Kind: m.Item.Kind, Size: m.Item.Size, Err: invalid})
			continue
		}
		if !e.recv || (c.Kind == KindImage && !e.images) {
			continue
		}
		e.apply(ctx, c, m.Item.TS)
	}
}

// apply writes a received item to the local board unless it is an echo or
// loses against a concurrent local change.
func (e *engine) apply(ctx context.Context, c Content, ts int64) {
	// Pure Lamport ordering: wall clocks play no part, so a host with a
	// wrong clock cannot make its stale change beat a newer one. The bound
	// only stops a hostile peer from overflowing the counter.
	ts = min(max(ts, 0), maxLamport)
	h := c.Hash()

	e.mu.Lock()
	defer e.mu.Unlock()
	e.clock = max(e.clock, ts)
	if h == e.last {
		return
	}
	remote := stamp{ts, 1 - e.rank()}
	if e.haveSent && !e.sent.less(remote) {
		return // our own, later change wins on both ends
	}
	if e.commit != nil {
		e.commit(e.dirIn, c)
	}
	if err := e.board.Write(ctx, c); err != nil {
		e.emit(Transfer{Dir: e.dirIn, Kind: c.Kind, Size: len(c.Data), Hash: h, Err: err})
		return
	}
	e.last = h
	e.emit(Transfer{Dir: e.dirIn, Kind: c.Kind, Size: len(c.Data), Hash: h, Content: c})
}
