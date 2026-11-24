package clip

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// helperTimeout bounds every helper process so a wedged display server can
// never hang a clipboard operation (or the host's sync loop) forever. It is a
// variable only so tests can shorten it.
var helperTimeout = 10 * time.Second

// capWriter collects up to max bytes and fails the write beyond that, which
// makes the helper's output pipe error out instead of buffering unbounded.
type capWriter struct {
	buf bytes.Buffer
	max int
}

var errCapExceeded = errors.New("output limit exceeded")

func (w *capWriter) Write(p []byte) (int, error) {
	if w.buf.Len()+len(p) > w.max {
		return 0, errCapExceeded
	}
	return w.buf.Write(p)
}

// helper runs external clipboard tools with a fixed environment.
type helper struct {
	env []string // nil means inherit
}

// output runs name and returns its stdout and (truncated) stderr. A non-zero
// exit is returned as an error alongside whatever was printed.
func (h helper) output(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, string, error) {
	ctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = h.env
	cmd.WaitDelay = time.Second
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out := &capWriter{max: MaxSize + 1}
	errw := &capWriter{max: 4096}
	cmd.Stdout = out
	cmd.Stderr = errw
	err := cmd.Run()
	if errors.Is(err, errCapExceeded) || out.buf.Len() > MaxSize {
		return nil, "", ErrTooLarge
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, "", fmt.Errorf("%s timed out after %s", name, helperTimeout)
	}
	return out.buf.Bytes(), strings.TrimSpace(errw.buf.String()), err
}

// own runs a tool that takes over the selection and keeps running to serve
// it (xclip, xsel and wl-copy fork into the background for that). Their
// stdout and stderr must not be pipes: a forked owner would inherit the pipe
// and Wait would block until the selection is lost. Stdout is /dev/null and
// stderr a temporary file, which the owner may keep open harmlessly and
// which still gives a failure its message. The foreground process exits
// once it has read stdin, which is all we wait for; the timeout is a
// backstop only.
func (h helper) own(ctx context.Context, stdin []byte, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, helperTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = h.env
	cmd.WaitDelay = time.Second
	cmd.Stdin = bytes.NewReader(stdin)
	errf, ferr := os.CreateTemp("", "remohop-clip-*.err")
	if ferr == nil {
		os.Remove(errf.Name()) // unlinked: nothing is left behind, even if we crash
		defer errf.Close()
		cmd.Stderr = errf
	}
	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%s timed out after %s", name, helperTimeout)
		}
		if errf != nil {
			msg := make([]byte, 512)
			n, _ := errf.ReadAt(msg, 0)
			if s := strings.TrimSpace(string(msg[:n])); s != "" {
				return fmt.Errorf("%s: %w: %s", name, err, s)
			}
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
