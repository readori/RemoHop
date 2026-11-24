package unattended

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"

	"github.com/readori/RemoHop/internal/inject"
	"github.com/readori/RemoHop/internal/svc/screenshot"
)

const (
	// helperIdleExit ends a helper nobody has used for this long (the
	// service normally ends it sooner).
	helperIdleExit = 3 * time.Minute
	// handshakeTimeout bounds connection set-up.
	handshakeTimeout = 10 * time.Second
	// captureGiveUp ends a stream that has not captured for this long.
	captureGiveUp = 20 * time.Second
	// layoutCheck is how often a stream checks that its display is still
	// there with the same geometry.
	layoutCheck = 2 * time.Second
)

// isLocalSystem reports whether this process runs as LocalSystem.
func isLocalSystem() bool {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	return err == nil && u.User.Sid.IsWellKnown(windows.WinLocalSystemSid)
}

type helper struct {
	cfg    helperConfig
	secret []byte
	log    io.Writer

	input *deskThread // the RPC connections' thread

	active   atomic.Int32
	lastUsed atomic.Int64

	noteMu   sync.Mutex
	lastDesk string
	session  uint32
}

// RunHelper is `remohop service session-helper`: the process the service starts
// in the console session. It reads its configuration from stdin (a pipe from
// the service), serves the service on its pipe and returns when the service
// goes away or it has been idle for a while. log receives diagnostics, which
// the service copies into its own log.
func RunHelper(ctx context.Context, stdin io.Reader, log io.Writer) error {
	if !isLocalSystem() {
		return errors.New("the session helper is started by the RemoHop service with unattended access; it does nothing on its own")
	}
	line, err := readLine(stdin, 4096, 10*time.Second)
	if err != nil {
		return fmt.Errorf("reading the configuration from the service: %w", err)
	}
	cfg, secret, err := parseHelperConfig(line)
	if err != nil {
		return err
	}
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil {
		return err
	}
	if session != cfg.Session {
		return fmt.Errorf("started in session %d, expected session %d", session, cfg.Session)
	}
	// End with the service, however it ends.
	parent, err := windows.OpenProcess(windows.SYNCHRONIZE, false, cfg.Parent)
	if err != nil {
		return fmt.Errorf("cannot watch the service process %d: %w", cfg.Parent, err)
	}
	go func() {
		windows.WaitForSingleObject(parent, windows.INFINITE)
		os.Exit(0)
	}()
	// Physical pixels, as the service and the clients count them.
	procSetProcessDPIAware.Call()

	l, err := listenPipe(cfg.Pipe)
	if err != nil {
		return fmt.Errorf("creating the helper pipe: %w", err)
	}
	defer l.Close()
	h := &helper{cfg: cfg, secret: secret, log: log, session: session}
	h.input = startDeskThread(h.noteDesktop)
	defer h.input.stop()
	h.lastUsed.Store(time.Now().UnixNano())
	fmt.Fprintf(log, "ready in session %d\n", session)

	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				l.Close()
				return
			case <-t.C:
				if h.active.Load() == 0 && time.Since(time.Unix(0, h.lastUsed.Load())) > helperIdleExit {
					fmt.Fprintln(log, "idle, exiting")
					l.Close()
					return
				}
			}
		}
	}()
	for {
		c, err := l.Accept()
		if err != nil {
			if errors.Is(err, errPipeClosed) {
				return nil
			}
			fmt.Fprintf(log, "accept: %v\n", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go h.serve(c)
	}
}

// readLine reads up to a newline from r, at most max bytes, within d.
func readLine(r io.Reader, max int, d time.Duration) ([]byte, error) {
	type res struct {
		b   []byte
		err error
	}
	ch := make(chan res, 1)
	go func() {
		b, err := bufio.NewReaderSize(io.LimitReader(r, int64(max)), max).ReadSlice('\n')
		ch <- res{b, err}
	}()
	select {
	case x := <-ch:
		return x.b, x.err
	case <-time.After(d):
		return nil, errors.New("timed out")
	}
}

// noteDesktop logs when the user's view moves to another desktop (each
// thread follows on its own; the log says it once).
func (h *helper) noteDesktop(name string) {
	h.noteMu.Lock()
	defer h.noteMu.Unlock()
	if name != h.lastDesk {
		h.lastDesk = name
		fmt.Fprintf(h.log, "input desktop: %s (%s)\n", name, DesktopKind(name))
	}
}

func (h *helper) serve(c *pipeConn) {
	defer c.Close()
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(h.log, "connection: panic: %v\n", r)
		}
	}()
	// Only the service that started this helper.
	if pid, err := c.clientPID(); err != nil || pid != h.cfg.Parent {
		fmt.Fprintf(h.log, "refused a connection from process %d (not the service, %d): %v\n", pid, h.cfg.Parent, err)
		return
	}
	stop := c.closeAfter(handshakeTimeout)
	err := serverHandshake(c, h.secret)
	var req request
	if err == nil {
		err = readJSON(c, msgRequest, &req)
	}
	if !stop() || err != nil {
		fmt.Fprintf(h.log, "refused a connection: %v\n", err)
		return
	}
	h.active.Add(1)
	defer func() {
		h.lastUsed.Store(time.Now().UnixNano())
		h.active.Add(-1)
	}()
	switch req.Op {
	case opRPC:
		h.serveRPC(c)
	case opStream:
		h.serveStream(c, req)
	case opGrab:
		h.serveGrab(c, req)
	default:
		writeJSON(c, msgReply, reply{Error: fmt.Sprintf("unknown operation %q", req.Op)})
	}
}

// serveRPC answers calls one at a time until the service hangs up.
func (h *helper) serveRPC(c *pipeConn) {
	sys := inject.LocalSys()
	for {
		var req request
		if err := readJSON(c, msgRequest, &req); err != nil {
			return
		}
		h.lastUsed.Store(time.Now().UnixNano())
		var rep reply
		err := h.input.do(func() error {
			switch req.Op {
			case opDisplays:
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				ds, err := screenshot.LocalDisplays(ctx)
				rep.Displays = ds
				return err
			case opMetric:
				rep.Value = int64(sys.Metric(req.Index))
			case opCursor:
				x, y, err := sys.CursorPos()
				rep.X, rep.Y = x, y
				return err
			case opVkScan:
				rep.Value = int64(sys.VkKeyScan(req.Ch))
			case opMapVK:
				rep.Value = int64(sys.MapVirtualKey(req.Code, req.MapType))
			case opInput:
				return sys.SendInput(req.Events, req.Count, req.Size)
			case opLock:
				if r, _, err := procLockWorkStation.Call(); r == 0 {
					return fmt.Errorf("LockWorkStation: %v", err)
				}
			case opDesktop:
				// Answered below from the thread's state.
			default:
				return fmt.Errorf("unknown operation %q", req.Op)
			}
			return nil
		})
		rep.Desktop, rep.Session = h.input.name, h.session
		if err != nil {
			rep.Error = err.Error()
		}
		if err := writeJSON(c, msgReply, rep); err != nil {
			return
		}
	}
}

func validRect(r *rect) error {
	if r == nil || r.W <= 0 || r.H <= 0 || r.W > 16384 || r.H > 16384 || r.W*r.H*4 > maxMsg {
		return errors.New("bad capture area")
	}
	return nil
}

// grabRetry is how long a single screenshot keeps trying through a
// desktop switch before it reports the error.
const grabRetry = 3 * time.Second

// serveGrab captures one picture.
func (h *helper) serveGrab(c *pipeConn, req request) {
	if err := validRect(req.Rect); err != nil {
		writeJSON(c, msgReply, reply{Error: err.Error()})
		return
	}
	r := *req.Rect
	t := startDeskThread(h.noteDesktop)
	defer t.stop()
	var g gdiCapture
	buf := make([]byte, r.W*r.H*4)
	grab := func() error {
		defer g.free()
		return g.grab(t, r, req.Cursor, buf)
	}
	// During a desktop switch (locking, Ctrl+Alt+Del, UAC) the thread can
	// still be on the old desktop, where BitBlt is denied, or there is no
	// input desktop to open yet. Retry briefly: each attempt follows the
	// input desktop again.
	err := t.do(grab)
	for deadline := time.Now().Add(grabRetry); err != nil && time.Now().Before(deadline); {
		time.Sleep(100 * time.Millisecond)
		err = t.do(grab)
	}
	if err != nil {
		writeJSON(c, msgReply, reply{Error: err.Error(), Desktop: t.name})
		return
	}
	if writeJSON(c, msgReply, reply{W: r.W, H: r.H, Desktop: t.name}) == nil {
		writeMsg(c, msgData, buf)
	}
}

// serveStream captures at the requested rate until the service hangs up,
// sending each frame, or a short "same" message when nothing changed. A
// frame is captured only after the previous one has been taken, so a slow
// reader gets fresh pictures, not a backlog.
func (h *helper) serveStream(c *pipeConn, req request) {
	if err := validRect(req.Rect); err != nil {
		writeJSON(c, msgReply, reply{Error: err.Error()})
		return
	}
	r := *req.Rect
	fps := min(max(req.FPS, 1), 120)
	t := startDeskThread(h.noteDesktop)
	defer t.stop()
	var g gdiCapture
	defer t.do(func() error { g.free(); return nil })
	if err := writeJSON(c, msgReply, reply{W: r.W, H: r.H}); err != nil {
		return
	}
	cur, prev := make([]byte, r.W*r.H*4), make([]byte, r.W*r.H*4)
	havePrev := false
	period := time.Second / time.Duration(fps)
	lastOK, lastLayout := time.Now(), time.Now()
	for {
		start := time.Now()
		err := t.do(func() error { return g.grab(t, r, req.Cursor, cur) })
		switch {
		case err != nil:
			// A desktop switch in progress, a disconnected session:
			// keep showing the last picture for a while.
			if time.Since(lastOK) > captureGiveUp {
				writeJSON(c, msgEnd, reply{Error: "screen capture failed: " + err.Error()})
				return
			}
			if havePrev {
				err = writeMsg(c, msgSame, nil)
			} else {
				err = nil
			}
		case havePrev && bytes.Equal(cur, prev):
			lastOK = time.Now()
			err = writeMsg(c, msgSame, nil)
		default:
			lastOK = time.Now()
			err = writeMsg(c, msgFrame, cur)
			cur, prev = prev, cur
			havePrev = true
		}
		if err != nil {
			return
		}
		h.lastUsed.Store(time.Now().UnixNano())
		if time.Since(lastLayout) > layoutCheck {
			lastLayout = time.Now()
			if why := h.layoutChanged(t, r); why != "" {
				writeJSON(c, msgEnd, reply{Error: why})
				return
			}
		}
		if d := period - time.Since(start); d > 0 {
			time.Sleep(d)
		}
	}
}

// layoutChanged says why r no longer matches a display ("" if it does): the
// service must then pick up the new geometry.
func (h *helper) layoutChanged(t *deskThread, r rect) string {
	var ds []screenshot.Display
	err := t.do(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var err error
		ds, err = screenshot.LocalDisplays(ctx)
		return err
	})
	if err != nil {
		return "" // cannot tell; keep going
	}
	if rectMatches(r, ds) {
		return ""
	}
	return "the display layout changed"
}
