package clip

import (
	"errors"
	"fmt"
	"io"

	"github.com/readori/RemoHop/internal/wire"
)

// Service is the stream service name.
const Service = "clip"

const (
	opInfo = "info"
	opGet  = "get"
	opSet  = "set"
	opSync = "sync"
	// opHistory reads the host's own clipboard history; request.H selects
	// the action.
	opHistory = "history"
)

// frameData is the binary frame type carrying item bytes.
const frameData byte = 'D'

// chunkSize is the item payload carried per frame, small enough that a
// transfer in one direction never starves the other of the stream mutex for
// long.
const chunkSize = 1 << 20

// request is the opener's header.
type request struct {
	Op     string `json:"op"`
	Push   bool   `json:"push,omitempty"`   // sync: the client sends its changes
	Pull   bool   `json:"pull,omitempty"`   // sync: the host sends its changes
	Images bool   `json:"images,omitempty"` // sync: transfer images too
	// history: H is "list" (newest Limit entries), "get" (content of entry
	// ID) or "restore" (put entry ID on the host clipboard).
	H     string `json:"h,omitempty"`
	ID    string `json:"id,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

const (
	histList    = "list"
	histGet     = "get"
	histRestore = "restore"
)

// MaxRemoteList is the most entries one remote history listing returns;
// previewBytes is how much text each listed entry carries (the full text is
// fetched per entry).
const (
	MaxRemoteList = 300
	previewBytes  = 256
)

// itemMeta announces the frames that follow it.
type itemMeta struct {
	Kind Kind   `json:"kind"`
	Size int    `json:"size"`
	Hash string `json:"hash"`
	// TS orders concurrent sync changes (see the package documentation).
	TS int64 `json:"ts,omitempty"`
}

// message is the one JSON message type on a clip stream.
type message struct {
	Backend string    `json:"backend,omitempty"`
	Empty   bool      `json:"empty,omitempty"`
	Error   string    `json:"error,omitempty"`
	Item    *itemMeta `json:"item,omitempty"`
	// history replies
	Entries []Entry `json:"entries,omitempty"`
	Entry   *Entry  `json:"entry,omitempty"`
}

// writeItem writes m (with its item metadata filled in from c) and the data
// frames. Callers serialise writers on the stream.
func writeItem(w io.Writer, m message, c Content, ts int64) error {
	m.Item = &itemMeta{Kind: c.Kind, Size: len(c.Data), Hash: c.Hash(), TS: ts}
	if err := wire.WriteJSON(w, m); err != nil {
		return err
	}
	for off := 0; off < len(c.Data); off += chunkSize {
		end := min(off+chunkSize, len(c.Data))
		if err := wire.WriteFrame(w, frameData, c.Data[off:end]); err != nil {
			return err
		}
	}
	return nil
}

// errInvalidItem marks an item that arrived intact but is unacceptable
// (bad hash, wrong format, too large to keep). The stream is still in sync
// and usable afterwards.
type errInvalidItem struct{ reason string }

func (e *errInvalidItem) Error() string { return e.reason }

// readItem reads the frames announced by meta. A non-nil fatal error means
// the stream is unusable; a non-nil *errInvalidItem (returned as invalid)
// means the data was consumed but rejected. Metadata is never trusted: the
// MIME type is derived from the kind, the size is bounded before anything is
// allocated and the hash is recomputed.
func readItem(r io.Reader, meta *itemMeta) (c Content, invalid *errInvalidItem, fatal error) {
	if meta.Size < 0 || meta.Size > MaxSize {
		// Cannot skip an unbounded amount of data safely; give up.
		return Content{}, nil, fmt.Errorf("peer announced a %d byte clipboard item (limit %d)", meta.Size, MaxSize)
	}
	data := make([]byte, 0, meta.Size)
	var buf []byte
	for len(data) < meta.Size {
		typ, payload, err := wire.ReadFrame(r, buf)
		if err != nil {
			return Content{}, nil, err
		}
		if typ != frameData || len(payload) > meta.Size-len(data) {
			return Content{}, nil, errors.New("malformed clipboard item frame")
		}
		data = append(data, payload...)
		buf = payload[:0]
	}
	c = normalize(Content{Kind: meta.Kind, Data: data})
	if err := c.Validate(); err != nil {
		return Content{}, &errInvalidItem{err.Error()}, nil
	}
	if c.Hash() != meta.Hash {
		return Content{}, &errInvalidItem{"clipboard item hash mismatch"}, nil
	}
	return c, nil, nil
}
