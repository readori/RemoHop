package mount

import (
	"context"
	"io"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

const (
	// opTimeout bounds every remote operation, so a wedged session turns
	// into an I/O error instead of a hung process.
	opTimeout = 60 * time.Second
	// Readahead window bounds for sequential reads.
	raMin = 128 << 10
	raMax = 4 << 20
)

// readahead caches a window of a sequentially read file and prefetches
// the next one in the background.
type readahead struct {
	mu      sync.Mutex
	off     int64
	data    []byte
	eof     bool // data ends at end of file
	pend    *fetch
	nextOff int64
	seq     int
	window  int
	gen     int
}

type fetch struct {
	off  int64
	n    int
	done chan struct{}
	data []byte
	eof  bool
	err  error
}

func (r *readahead) invalidate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.gen++
	r.data, r.pend, r.eof = nil, nil, false
}

// hitLocked copies from the cache if it covers the request.
func (r *readahead) hitLocked(dest []byte, off int64) (int, bool) {
	end := r.off + int64(len(r.data))
	if r.data == nil || off < r.off || off > end || (off == end && !r.eof) {
		return 0, false
	}
	avail := end - off
	if avail < int64(len(dest)) && !r.eof {
		return 0, false
	}
	return copy(dest, r.data[off-r.off:]), true
}

func (r *readahead) read(ctx context.Context, f *rfs.File, dest []byte, off int64) (int, error) {
	r.mu.Lock()
	if off == r.nextOff {
		r.seq++
	} else {
		r.seq, r.window = 0, raMin
	}
	r.nextOff = off + int64(len(dest))
	if p := r.pend; p != nil && off >= p.off && off < p.off+int64(p.n) {
		r.mu.Unlock()
		select {
		case <-p.done:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
		r.mu.Lock()
		if r.pend == p {
			r.pend = nil
			if p.err == nil {
				r.off, r.data, r.eof = p.off, p.data, p.eof
			}
		}
	}
	if n, ok := r.hitLocked(dest, off); ok {
		r.prefetchLocked(f)
		r.mu.Unlock()
		return n, nil
	}
	if r.seq < 2 {
		r.mu.Unlock()
		n, err := f.ReadAt(ctx, dest, off)
		if err == io.EOF {
			err = nil
		}
		return n, err
	}
	// A sequential miss: fetch a whole window, growing it each time.
	size := max(r.window, len(dest))
	r.window = min(r.window*2, raMax)
	gen := r.gen
	r.mu.Unlock()
	buf := make([]byte, size)
	n, err := f.ReadAt(ctx, buf, off)
	if err != nil && err != io.EOF {
		return 0, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.gen == gen {
		r.off, r.data, r.eof, r.pend = off, buf[:n], err == io.EOF, nil
		r.prefetchLocked(f)
	}
	return copy(dest, buf[:n]), nil
}

// prefetchLocked starts fetching the window after the cache once the
// reader has consumed half of it.
func (r *readahead) prefetchLocked(f *rfs.File) {
	if r.pend != nil || r.eof || r.seq < 2 || r.data == nil {
		return
	}
	end := r.off + int64(len(r.data))
	if end-r.nextOff > int64(r.window/2) {
		return
	}
	p := &fetch{off: end, n: r.window, done: make(chan struct{})}
	r.pend = p
	go func() {
		defer close(p.done)
		ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
		defer cancel()
		buf := make([]byte, p.n)
		n, err := f.ReadAt(ctx, buf, p.off)
		p.data, p.eof = buf[:n], err == io.EOF
		if err != nil && err != io.EOF {
			p.err = err
		}
	}()
}
