package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/readori/RemoHop/internal/cli"
)

// maxLine bounds one log line; longer ones are cut rather than buffered
// without limit.
const maxLine = 64 << 10

// copySanitized copies r to w line by line, neutralising terminal control
// sequences: log lines can quote text a remote peer sent.
func copySanitized(w io.Writer, r io.Reader) {
	br := bufio.NewReaderSize(r, 4096)
	for {
		line, err := br.ReadSlice('\n')
		if len(line) > 0 {
			fmt.Fprint(w, cli.Sanitize(string(line)))
			if line[len(line)-1] != '\n' {
				fmt.Fprintln(w)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue // the rest of an over-long line follows as its own chunk
		}
		if err != nil {
			return
		}
	}
}

// tailFile prints the last n lines of path and, with follow, new lines as
// they are appended until ctx ends. It copes with rotation by truncation.
func tailFile(ctx context.Context, path string, n int, follow bool, w io.Writer) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	size := fi.Size()
	start := size - maxLine*16
	if start < 0 {
		start = 0
	}
	buf := make([]byte, size-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	copySanitized(w, bytes.NewReader(lastLines(buf, n, start > 0)))
	if !follow {
		return nil
	}
	pos := size
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(500 * time.Millisecond):
		}
		fi, err := f.Stat()
		if err != nil {
			return err
		}
		if fi.Size() < pos {
			pos = 0 // truncated
		}
		if fi.Size() == pos {
			continue
		}
		chunk := make([]byte, min(fi.Size()-pos, 1<<20))
		m, _ := f.ReadAt(chunk, pos)
		pos += int64(m)
		copySanitized(w, bytes.NewReader(chunk[:m]))
	}
}

// lastLines returns the final n lines of b. partial says b starts mid-line,
// in which case the first (incomplete) line is dropped.
func lastLines(b []byte, n int, partial bool) []byte {
	if partial {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	b = bytes.TrimRight(b, "\n")
	if n <= 0 || len(b) == 0 {
		return nil
	}
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == '\n' {
			if n--; n == 0 {
				return append(b[i+1:], '\n')
			}
		}
	}
	return append(b, '\n')
}
