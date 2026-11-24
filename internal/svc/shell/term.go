package shell

import (
	"context"
	"io"
	"os"

	"golang.org/x/term"
)

// localTerm is the controlling terminal of the CLI, when stdin is one.
type localTerm struct {
	f   *os.File // stdin
	out *os.File // stdout when it is the terminal too, else nil
}

// openTerm returns the local terminal behind stdin, or nil when stdin is not
// a terminal (a pipe, a file, a test buffer). stdout is remembered when it
// is a terminal: Windows reports the window size and takes output modes on
// the output handle, not the input one.
func openTerm(stdin io.Reader, stdout io.Writer) *localTerm {
	f, ok := stdin.(*os.File)
	if !ok || !term.IsTerminal(int(f.Fd())) {
		return nil
	}
	t := &localTerm{f: f}
	if o, ok := stdout.(*os.File); ok && term.IsTerminal(int(o.Fd())) {
		t.out = o
	}
	return t
}

func (t *localTerm) size() (rows, cols int) {
	for _, f := range []*os.File{t.out, t.f} {
		if f == nil {
			continue
		}
		if cols, rows, err := term.GetSize(int(f.Fd())); err == nil && rows > 0 && cols > 0 {
			return rows, cols
		}
	}
	return 24, 80
}

// makeRaw switches the terminal to raw mode and returns the function that
// restores it; calling that function more than once is harmless.
func (t *localTerm) makeRaw() (restore func(), err error) {
	st, err := term.MakeRaw(int(t.f.Fd()))
	if err != nil {
		return nil, err
	}
	restoreOut := func() {}
	if t.out != nil {
		restoreOut = enableVTOutput(t.out)
	}
	done := false
	return func() {
		if !done {
			done = true
			term.Restore(int(t.f.Fd()), st)
			restoreOut()
		}
	}, nil
}

// watchSize delivers the terminal's new size whenever it changes, until ctx
// ends.
func (t *localTerm) watchSize(ctx context.Context) <-chan WinSize {
	ch := make(chan WinSize, 1)
	go func() {
		changed, stop := sizeChanges()
		defer stop()
		lastRows, lastCols := t.size()
		for {
			select {
			case <-ctx.Done():
				return
			case <-changed:
				rows, cols := t.size()
				if rows == lastRows && cols == lastCols {
					continue
				}
				lastRows, lastCols = rows, cols
				// Keep only the latest size if the consumer lags.
				select {
				case <-ch:
				default:
				}
				ch <- WinSize{Rows: rows, Cols: cols}
			}
		}
	}()
	return ch
}
