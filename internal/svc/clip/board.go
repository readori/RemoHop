package clip

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Board is a system clipboard (or a stand-in for one).
type Board interface {
	// Name identifies the backend ("x11/xclip", "wayland", "memory", ...).
	Name() string
	// Read returns the current content, or ErrEmpty.
	Read(ctx context.Context) (Content, error)
	// Write replaces the content. The content is validated first.
	Write(ctx context.Context, c Content) error
	// Watch delivers content that changed after Watch was called, until ctx
	// ends, when the channel is closed. Clearing the clipboard is not a
	// change. Backends without change events poll.
	Watch(ctx context.Context) <-chan Content
}

// pollInterval is how often helper-based backends are polled for changes.
// Every poll runs a helper process, so this is a compromise between latency
// and load.
const pollInterval = 750 * time.Millisecond

// pollWatch implements Watch by reading b every interval and emitting the
// content whenever its hash differs from the previous read.
func pollWatch(ctx context.Context, b Board, interval time.Duration) <-chan Content {
	out := make(chan Content, 4)
	var last string
	read := func() (Content, bool) {
		c, err := b.Read(ctx)
		switch {
		case err == nil:
			return c, true
		case errors.Is(err, ErrEmpty):
			last = ""
		}
		return Content{}, false
	}
	// The baseline is taken before returning, so anything copied after
	// Watch returns is reported.
	if c, ok := read(); ok {
		last = c.Hash()
	}
	go func() {
		defer close(out)
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			c, ok := read()
			if !ok {
				continue
			}
			if h := c.Hash(); h != last {
				last = h
				select {
				case out <- c:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

// MemoryBoard is the virtual clipboard used when no system clipboard exists.
// It is event-driven and safe for concurrent use.
type MemoryBoard struct {
	mu   sync.Mutex
	cur  Content
	subs map[chan Content]struct{}
}

// NewMemoryBoard returns an empty in-memory clipboard.
func NewMemoryBoard() *MemoryBoard { return &MemoryBoard{subs: map[chan Content]struct{}{}} }

// Name implements Board.
func (m *MemoryBoard) Name() string { return "memory" }

// Read implements Board.
func (m *MemoryBoard) Read(context.Context) (Content, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.cur.Data) == 0 {
		return Content{}, ErrEmpty
	}
	return m.cur, nil
}

// Write implements Board.
func (m *MemoryBoard) Write(_ context.Context, c Content) error {
	c = normalize(c)
	if err := c.Validate(); err != nil {
		return err
	}
	c.Data = append([]byte(nil), c.Data...)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cur = c
	for ch := range m.subs {
		for {
			select {
			case ch <- c:
			default:
				// Subscriber is behind: drop its oldest item so the
				// newest always gets through.
				select {
				case <-ch:
				default:
				}
				continue
			}
			break
		}
	}
	return nil
}

// Watch implements Board.
func (m *MemoryBoard) Watch(ctx context.Context) <-chan Content {
	ch := make(chan Content, 16)
	m.mu.Lock()
	m.subs[ch] = struct{}{}
	m.mu.Unlock()
	go func() {
		<-ctx.Done()
		m.mu.Lock()
		delete(m.subs, ch)
		close(ch)
		m.mu.Unlock()
	}()
	return ch
}
