package unattended

import (
	"context"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/readori/RemoHop/internal/inject"
	"github.com/readori/RemoHop/internal/svc/screenshot"
)

const (
	// idleStop ends a helper that nothing has used for this long.
	idleStop = 60 * time.Second
	// monitorEvery is how often the service checks which session is on
	// the console.
	monitorEvery = time.Second
	// callTimeout bounds one call to the helper.
	callTimeout = 10 * time.Second
	// launchTimeout bounds starting a helper until its pipe answers.
	launchTimeout = 20 * time.Second
	// metricLife is how long screen metrics are reused (each SendInput of
	// a pointer move needs four).
	metricLife = 500 * time.Millisecond
)

// ErrNotActive is returned when the process is not a service with
// unattended access.
var ErrNotActive = errors.New("unattended access is not enabled in this process")

var current atomic.Pointer[broker]

// Active reports whether this process is the Windows service with unattended
// access enabled: capture and input then go to the console session.
func Active() bool { return current.Load() != nil }

type broker struct {
	log  io.Writer
	mode string
	job  windows.Handle
	stop chan struct{}

	mu       sync.Mutex
	h        *helperProc
	fails    int
	nextTry  time.Time
	target   uint32
	targetOK bool
	targetAt time.Time
	users    int // open streams and grabs
	lastUse  time.Time

	rpcMu     sync.Mutex
	rpc       *pipeConn
	rpcHelper *helperProc

	metricMu sync.Mutex
	metrics  map[int]metricVal
}

type metricVal struct {
	v  int
	at time.Time
}

// Enable makes this process (the Windows service) show and control the
// console session: input injection (internal/inject), screenshots and the
// display list (internal/svc/screenshot) and the desktop service's capture go
// through a helper in the console session from now on. log receives what
// the helper reports. stop undoes it and ends the helper.
func Enable(log io.Writer) (stop func(), err error) {
	if !isLocalSystem() {
		return nil, errors.New("unattended access needs the service to run as LocalSystem")
	}
	b := &broker{log: log, mode: os.Getenv(SessionEnv), stop: make(chan struct{}), metrics: map[int]metricVal{}, lastUse: time.Now()}
	if b.job, err = newKillJob(); err != nil {
		fmt.Fprintf(log, "unattended: no job object for helpers (%v); they end on their own when the service does\n", err)
	}
	if !current.CompareAndSwap(nil, b) {
		if b.job != 0 {
			windows.CloseHandle(b.job)
		}
		return nil, errors.New("unattended access is already enabled")
	}
	inject.UseSys(b)
	screenshot.UseSessionGrabber(b)
	go b.monitor()
	id, ok := b.currentTarget(true)
	fmt.Fprintf(log, "unattended: enabled; console session %d (available: %v), mode %q; Ctrl+Alt+Del: %s\n", id, ok, b.modeName(), sasState())
	var once sync.Once
	return func() {
		once.Do(func() {
			close(b.stop)
			inject.UseSys(nil)
			screenshot.UseSessionGrabber(nil)
			current.CompareAndSwap(b, nil)
			b.mu.Lock()
			if b.h != nil {
				b.h.kill()
				b.h = nil
			}
			b.mu.Unlock()
			b.rpcMu.Lock()
			if b.rpc != nil {
				b.rpc.Close()
				b.rpc = nil
			}
			b.rpcMu.Unlock()
			if b.job != 0 {
				windows.CloseHandle(b.job)
			}
		})
	}, nil
}

func (b *broker) modeName() string {
	if b.mode == "" {
		return "console"
	}
	return b.mode
}

func sasState() string {
	if ok, why := SASAvailable(); !ok {
		return "unavailable (" + why + ")"
	}
	return "available"
}

// sessions lists the sessions (for SessionEnv=active).
func listSessions() []sessionInfo {
	var p *windows.WTS_SESSION_INFO
	var n uint32
	if err := windows.WTSEnumerateSessions(0, 0, 1, &p, &n); err != nil {
		return nil
	}
	defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(p)))
	out := make([]sessionInfo, 0, n)
	for _, s := range unsafe.Slice(p, n) {
		out = append(out, sessionInfo{ID: s.SessionID, State: s.State, Name: windows.UTF16PtrToString(s.WindowStationName)})
	}
	return out
}

// currentTarget is the session to show, recomputed when stale or asked.
func (b *broker) currentTarget(fresh bool) (uint32, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.targetLocked(fresh)
}

func (b *broker) targetLocked(fresh bool) (uint32, bool) {
	if fresh || time.Since(b.targetAt) > monitorEvery {
		var list []sessionInfo
		if b.mode != "" && b.mode != "console" {
			list = listSessions()
		}
		b.target, b.targetOK = pickSession(b.mode, windows.WTSGetActiveConsoleSessionId(), list)
		b.targetAt = time.Now()
	}
	return b.target, b.targetOK
}

// monitor follows the console: the helper of a session that is no longer
// shown is ended (its streams end with it and the desktop sessions start
// capturing again, in the new session), and an idle helper is ended.
func (b *broker) monitor() {
	t := time.NewTicker(monitorEvery)
	defer t.Stop()
	for {
		select {
		case <-b.stop:
			return
		case <-t.C:
		}
		b.mu.Lock()
		target, ok := b.targetLocked(true)
		if h := b.h; h != nil {
			switch {
			case !h.alive():
				b.h = nil
			case !ok || h.session != target:
				if ok {
					fmt.Fprintf(b.log, "unattended: the console moved from session %d to session %d\n", h.session, target)
				} else {
					fmt.Fprintf(b.log, "unattended: session %d is no longer on the console\n", h.session)
				}
				h.kill()
				b.h = nil
			case b.users == 0 && time.Since(b.lastUse) > idleStop:
				fmt.Fprintf(b.log, "unattended: helper of session %d idle, ending it\n", h.session)
				h.kill()
				b.h = nil
			}
		}
		b.mu.Unlock()
	}
}

// helper returns a running helper in the session to show, starting one if
// needed.
func (b *broker) helper(ctx context.Context) (*helperProc, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lastUse = time.Now()
	target, ok := b.targetLocked(false)
	if !ok {
		return nil, errors.New("no interactive session is attached to the console right now")
	}
	if h := b.h; h != nil {
		if h.alive() && h.session == target {
			return h, nil
		}
		h.kill()
		b.h = nil
	}
	if wait := time.Until(b.nextTry); wait > 0 {
		return nil, fmt.Errorf("the session helper failed to start; retrying in %v", wait.Round(time.Second))
	}
	h, err := launchHelper(target, b.job, b.log)
	if err == nil {
		err = b.waitReady(ctx, h)
		if err != nil {
			h.kill()
		}
	}
	if err != nil {
		b.fails++
		b.nextTry = time.Now().Add(relaunchDelay(b.fails))
		fmt.Fprintf(b.log, "unattended: %v\n", err)
		return nil, err
	}
	b.fails = 0
	b.h = h
	fmt.Fprintf(b.log, "unattended: session helper started in session %d (pid %d)\n", h.session, h.pid)
	return h, nil
}

// waitReady waits for a new helper's pipe.
func (b *broker) waitReady(ctx context.Context, h *helperProc) error {
	ctx, cancel := context.WithTimeout(ctx, launchTimeout)
	defer cancel()
	go func() {
		select {
		case <-h.exited:
			cancel()
		case <-ctx.Done():
		}
	}()
	c, err := b.dialHelper(ctx, h, request{Op: opRPC})
	if err != nil {
		if !h.alive() {
			return fmt.Errorf("the session helper exited during start-up (see its messages above)")
		}
		return fmt.Errorf("the session helper did not answer: %w", err)
	}
	c.Close()
	return nil
}

// dialHelper opens a connection to h and sends its first request.
func (b *broker) dialHelper(ctx context.Context, h *helperProc, first request) (*pipeConn, error) {
	c, err := dialPipe(ctx, h.pipe)
	if err != nil {
		return nil, err
	}
	// The pipe must be served by the helper this service started.
	if pid, err := c.serverPID(); err != nil || pid != h.pid {
		c.Close()
		return nil, fmt.Errorf("the helper pipe is served by process %d, not the helper (%d): %v", pid, h.pid, err)
	}
	stop := c.closeAfter(handshakeTimeout)
	err = clientHandshake(c, h.secret)
	if err == nil {
		err = writeJSON(c, msgRequest, first)
	}
	if !stop() && err == nil {
		err = errors.New("handshake timed out")
	}
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// call makes one call on the shared RPC connection, reconnecting once if
// the helper was replaced or the connection broke.
func (b *broker) call(req request) (reply, error) {
	// Room for starting a helper first; the call itself has callTimeout.
	ctx, cancel := context.WithTimeout(context.Background(), launchTimeout+callTimeout)
	defer cancel()
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		h, err := b.helper(ctx)
		if err != nil {
			return reply{}, err
		}
		b.rpcMu.Lock()
		if b.rpc != nil && b.rpcHelper != h {
			b.rpc.Close()
			b.rpc = nil
		}
		if b.rpc == nil {
			c, err := b.dialHelper(ctx, h, request{Op: opRPC})
			if err != nil {
				b.rpcMu.Unlock()
				lastErr = err
				continue
			}
			b.rpc, b.rpcHelper = c, h
		}
		c := b.rpc
		stop := c.closeAfter(callTimeout)
		var rep reply
		err = writeJSON(c, msgRequest, req)
		if err == nil {
			err = readJSON(c, msgReply, &rep)
		}
		stop()
		if err != nil {
			c.Close()
			b.rpc = nil
			b.rpcMu.Unlock()
			lastErr = err
			continue
		}
		b.rpcMu.Unlock()
		return rep, rep.err()
	}
	return reply{}, fmt.Errorf("unattended: the session helper does not answer: %w", lastErr)
}

func (b *broker) use(delta int) {
	b.mu.Lock()
	b.users += delta
	b.lastUse = time.Now()
	b.mu.Unlock()
}

// inject.Sys

func (b *broker) SendInput(events []byte, count, size int) error {
	_, err := b.call(request{Op: opInput, Events: events, Count: count, Size: size})
	return err
}

func (b *broker) Metric(index int) int {
	b.metricMu.Lock()
	if m, ok := b.metrics[index]; ok && time.Since(m.at) < metricLife {
		b.metricMu.Unlock()
		return m.v
	}
	b.metricMu.Unlock()
	rep, err := b.call(request{Op: opMetric, Index: index})
	if err != nil {
		return 0
	}
	b.metricMu.Lock()
	b.metrics[index] = metricVal{int(rep.Value), time.Now()}
	b.metricMu.Unlock()
	return int(rep.Value)
}

func (b *broker) CursorPos() (int, int, error) {
	rep, err := b.call(request{Op: opCursor})
	return rep.X, rep.Y, err
}

func (b *broker) VkKeyScan(ch uint16) int16 {
	rep, err := b.call(request{Op: opVkScan, Ch: ch})
	if err != nil {
		return -1
	}
	return int16(rep.Value)
}

func (b *broker) MapVirtualKey(code, mapType uint32) uint32 {
	rep, err := b.call(request{Op: opMapVK, Code: code, MapType: mapType})
	if err != nil {
		return 0
	}
	return uint32(rep.Value)
}

// SendSAS implements inject.SASSender: the service itself raises
// Ctrl+Alt+Del.
func (b *broker) SendSAS() error { return SendSAS() }

// screenshot.SessionGrabber

func (b *broker) Displays(ctx context.Context) ([]screenshot.Display, error) {
	rep, err := b.call(request{Op: opDisplays})
	if err != nil {
		return nil, err
	}
	if len(rep.Displays) == 0 {
		return nil, errors.New("no display available in the console session")
	}
	return rep.Displays, nil
}

func (b *broker) Grab(ctx context.Context, display int) (image.Image, error) {
	ds, err := b.Displays(ctx)
	if err != nil {
		return nil, err
	}
	r, err := displayArea(ds, display)
	if err != nil {
		return nil, err
	}
	pix, err := b.grabRect(ctx, r, false)
	if err != nil {
		return nil, err
	}
	img := &image.RGBA{Pix: pix, Stride: r.W * 4, Rect: image.Rect(0, 0, r.W, r.H)}
	for i := 0; i < len(pix); i += 4 {
		pix[i], pix[i+2], pix[i+3] = pix[i+2], pix[i], 255
	}
	return img, nil
}

// grabRect captures r once, as BGRX.
func (b *broker) grabRect(ctx context.Context, r rect, cursor bool) ([]byte, error) {
	b.use(1)
	defer b.use(-1)
	ctx, cancel := context.WithTimeout(ctx, launchTimeout+callTimeout)
	defer cancel()
	h, err := b.helper(ctx)
	if err != nil {
		return nil, err
	}
	c, err := b.dialHelper(ctx, h, request{Op: opGrab, Rect: &r, Cursor: cursor})
	if err != nil {
		return nil, err
	}
	defer c.Close()
	stop := c.closeAfter(callTimeout + 5*time.Second)
	defer stop()
	var rep reply
	if err := readJSON(c, msgReply, &rep); err != nil {
		return nil, err
	}
	if err := rep.err(); err != nil {
		return nil, fmt.Errorf("screen capture in the console session: %w", err)
	}
	typ, pix, err := readMsg(c, nil)
	if err != nil {
		return nil, err
	}
	if typ != msgData || len(pix) != r.W*r.H*4 {
		return nil, errors.New("unattended: bad capture from the session helper")
	}
	return pix, nil
}

// Displays lists the console session's monitors (as the screenshot service
// numbers them).
func Displays(ctx context.Context) ([]screenshot.Display, error) {
	b := current.Load()
	if b == nil {
		return nil, ErrNotActive
	}
	return b.Displays(ctx)
}

// Stream is a running capture of an area of the console session.
type Stream struct {
	// W, H is the size of each frame; frames are 32-bit BGRX pixels,
	// top row first (ffmpeg's rawvideo pixel format "bgr0").
	W, H int
	b    *broker
	c    *pipeConn
	once sync.Once
}

// OpenStream starts capturing area r (screen pixels of the console
// session's virtual desktop) at fps frames per second, with the pointer
// drawn in.
func OpenStream(ctx context.Context, r image.Rectangle, fps int) (*Stream, error) {
	b := current.Load()
	if b == nil {
		return nil, ErrNotActive
	}
	ar := rect{X: r.Min.X, Y: r.Min.Y, W: r.Dx(), H: r.Dy()}
	if err := validRect(&ar); err != nil {
		return nil, err
	}
	b.use(1)
	h, err := b.helper(ctx)
	var c *pipeConn
	if err == nil {
		c, err = b.dialHelper(ctx, h, request{Op: opStream, Rect: &ar, FPS: fps, Cursor: true})
	}
	if err != nil {
		b.use(-1)
		return nil, err
	}
	stop := c.closeAfter(callTimeout)
	var rep reply
	err = readJSON(c, msgReply, &rep)
	if !stop() && err == nil {
		err = errors.New("timed out")
	}
	if err == nil {
		err = rep.err()
	}
	if err == nil && (rep.W != ar.W || rep.H != ar.H) {
		err = fmt.Errorf("the helper captures %dx%d, not %dx%d", rep.W, rep.H, ar.W, ar.H)
	}
	if err != nil {
		c.Close()
		b.use(-1)
		return nil, fmt.Errorf("screen capture in the console session: %w", err)
	}
	return &Stream{W: ar.W, H: ar.H, b: b, c: c}, nil
}

// Copy writes frames to dst until the stream ends (the session left the
// console, the display layout changed, Close) or dst fails. It returns nil
// only when the helper ended the stream without an error.
func (s *Stream) Copy(dst io.Writer) error {
	return copyFrames(dst, s.c, s.W*s.H*4)
}

// Close ends the stream.
func (s *Stream) Close() {
	s.once.Do(func() {
		s.c.Close()
		s.b.use(-1)
	})
}

// Lock locks the console session.
func Lock(ctx context.Context) error {
	b := current.Load()
	if b == nil {
		return ErrNotActive
	}
	_, err := b.call(request{Op: opLock})
	return err
}

// InputDesktop names the desktop that receives input in the console session
// ("Default", "Winlogon", ...), as the helper last saw it.
func InputDesktop() (string, error) {
	b := current.Load()
	if b == nil {
		return "", ErrNotActive
	}
	rep, err := b.call(request{Op: opDesktop})
	return rep.Desktop, err
}
