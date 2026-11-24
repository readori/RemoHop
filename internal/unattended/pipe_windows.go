package unattended

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The helper's pipe: overlapped I/O, so that a blocked read or write ends
// when the connection is closed from another goroutine (os.File cannot do
// that for a pipe handle on Windows).

// pipeSDDL admits LocalSystem alone, not inherited, so neither the user of
// the session nor an administrator's process can open the pipe.
const pipeSDDL = "O:SYD:P(A;;GA;;;SY)"

var errPipeClosed = errors.New("unattended: pipe closed")

// pipeConn is one connection. The OVERLAPPED structures and the buffers the
// kernel reads into and writes from belong to the connection, on the heap:
// a goroutine's stack can move while an operation is pending, the heap does
// not.
type pipeConn struct {
	closed  atomic.Bool
	h       windows.Handle
	closeEv windows.Handle
	once    sync.Once
	rmu     sync.Mutex
	wmu     sync.Mutex
	rov     windows.Overlapped
	wov     windows.Overlapped
	rbuf    []byte
	wbuf    []byte
}

const pipeChunk = 1 << 20

func newPipeConn(h windows.Handle) (*pipeConn, error) {
	c := &pipeConn{h: h, rbuf: make([]byte, pipeChunk), wbuf: make([]byte, pipeChunk)}
	var err error
	for _, ev := range []*windows.Handle{&c.closeEv, &c.rov.HEvent, &c.wov.HEvent} {
		if *ev, err = windows.CreateEvent(nil, 1, 0, nil); err != nil {
			c.h = 0 // the caller still owns it
			c.free()
			return nil, err
		}
	}
	return c, nil
}

func (c *pipeConn) free() {
	for _, h := range []windows.Handle{c.h, c.closeEv, c.rov.HEvent, c.wov.HEvent} {
		if h != 0 {
			windows.CloseHandle(h)
		}
	}
}

// wait waits for an overlapped operation, cancelling it if the connection
// is closed first.
func (c *pipeConn) wait(ov *windows.Overlapped) (uint32, error) {
	ev, err := windows.WaitForMultipleObjects([]windows.Handle{ov.HEvent, c.closeEv}, false, windows.INFINITE)
	var n uint32
	if err != nil || ev != windows.WAIT_OBJECT_0 {
		windows.CancelIoEx(c.h, ov)
		windows.GetOverlappedResult(c.h, ov, &n, true)
		return 0, errPipeClosed
	}
	err = windows.GetOverlappedResult(c.h, ov, &n, false)
	return n, err
}

// start resets ov for a new operation.
func start(ov *windows.Overlapped) {
	ev := ov.HEvent
	*ov = windows.Overlapped{HEvent: ev}
	windows.ResetEvent(ev)
}

func (c *pipeConn) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	c.rmu.Lock()
	defer c.rmu.Unlock()
	if c.isClosed() {
		return 0, errPipeClosed
	}
	buf := c.rbuf[:min(len(b), len(c.rbuf))]
	start(&c.rov)
	var n uint32
	err := windows.ReadFile(c.h, buf, &n, &c.rov)
	if err == windows.ERROR_IO_PENDING {
		n, err = c.wait(&c.rov)
	}
	switch {
	case err == windows.ERROR_BROKEN_PIPE || err == windows.ERROR_PIPE_NOT_CONNECTED:
		return 0, io.EOF
	case err != nil:
		return copy(b, buf[:n]), err
	case n == 0:
		return 0, io.EOF
	}
	return copy(b, buf[:n]), nil
}

func (c *pipeConn) Write(b []byte) (int, error) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	done := 0
	for done < len(b) {
		if c.isClosed() {
			return done, errPipeClosed
		}
		k := copy(c.wbuf, b[done:])
		start(&c.wov)
		var n uint32
		err := windows.WriteFile(c.h, c.wbuf[:k], &n, &c.wov)
		if err == windows.ERROR_IO_PENDING {
			n, err = c.wait(&c.wov)
		}
		done += int(n)
		if err != nil {
			if err == windows.ERROR_NO_DATA || err == windows.ERROR_BROKEN_PIPE {
				err = io.ErrClosedPipe
			}
			return done, err
		}
	}
	return done, nil
}

func (c *pipeConn) isClosed() bool { return c.closed.Load() }

// Close ends the connection; blocked reads and writes return.
func (c *pipeConn) Close() error {
	c.once.Do(func() {
		c.closed.Store(true)
		windows.SetEvent(c.closeEv)
		c.rmu.Lock()
		c.wmu.Lock()
		c.free()
		c.wmu.Unlock()
		c.rmu.Unlock()
	})
	return nil
}

// closeAfter closes the connection unless stop is called within d (a
// deadline for the handshake).
func (c *pipeConn) closeAfter(d time.Duration) (stop func() bool) {
	return time.AfterFunc(d, func() { c.Close() }).Stop
}

// clientPID and serverPID are the process IDs at the two ends.
func (c *pipeConn) clientPID() (uint32, error) {
	var pid uint32
	err := windows.GetNamedPipeClientProcessId(c.h, &pid)
	return pid, err
}

func (c *pipeConn) serverPID() (uint32, error) {
	var pid uint32
	err := windows.GetNamedPipeServerProcessId(c.h, &pid)
	return pid, err
}

// pipeListener is the helper's end. It always keeps one instance of the
// pipe waiting, created before the previous one is handed out, so that the
// name never lapses while the helper runs and nobody else can take it over.
type pipeListener struct {
	name    *uint16
	sa      *windows.SecurityAttributes
	closeEv windows.Handle

	mu   sync.Mutex
	next windows.Handle
	done bool
	// ov is the pending ConnectNamedPipe's (Accept is called by one
	// goroutine at a time).
	ov windows.Overlapped
}

func listenPipe(name string) (*pipeListener, error) {
	sd, err := windows.SecurityDescriptorFromString(pipeSDDL)
	if err != nil {
		return nil, err
	}
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	l := &pipeListener{name: p, sa: &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}}
	if l.closeEv, err = windows.CreateEvent(nil, 1, 0, nil); err != nil {
		return nil, err
	}
	// The first instance must be new: if the name exists, someone else
	// made it and this helper must not serve on it.
	if l.next, err = l.create(true); err != nil {
		windows.CloseHandle(l.closeEv)
		return nil, err
	}
	return l, nil
}

func (l *pipeListener) create(first bool) (windows.Handle, error) {
	flags := uint32(windows.PIPE_ACCESS_DUPLEX | windows.FILE_FLAG_OVERLAPPED)
	if first {
		flags |= windows.FILE_FLAG_FIRST_PIPE_INSTANCE
	}
	return windows.CreateNamedPipe(l.name, flags,
		windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		windows.PIPE_UNLIMITED_INSTANCES, 1<<20, 1<<20, 0, l.sa)
}

// Accept waits for the next client.
func (l *pipeListener) Accept() (*pipeConn, error) {
	l.mu.Lock()
	h := l.next
	l.next = 0
	done := l.done
	l.mu.Unlock()
	if done || h == 0 {
		if h != 0 {
			windows.CloseHandle(h)
		}
		return nil, errPipeClosed
	}
	err := l.connect(h)
	// The next instance exists before this one is handed out (or
	// dropped), so the name never lapses.
	nh, cerr := l.create(false)
	l.mu.Lock()
	switch {
	case l.done && cerr == nil:
		windows.CloseHandle(nh)
	case cerr == nil:
		l.next = nh
	}
	done = l.done
	l.mu.Unlock()
	if err != nil || done {
		windows.CloseHandle(h)
		if done {
			return nil, errPipeClosed
		}
		return nil, err
	}
	if cerr != nil {
		windows.CloseHandle(h)
		return nil, cerr
	}
	c, err := newPipeConn(h)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return c, nil
}

// connect waits for a client on instance h.
func (l *pipeListener) connect(h windows.Handle) error {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ev)
	l.ov = windows.Overlapped{HEvent: ev}
	ov := &l.ov
	err = windows.ConnectNamedPipe(h, ov)
	if err == windows.ERROR_IO_PENDING {
		w, werr := windows.WaitForMultipleObjects([]windows.Handle{ev, l.closeEv}, false, windows.INFINITE)
		var n uint32
		if werr != nil || w != windows.WAIT_OBJECT_0 {
			windows.CancelIoEx(h, ov)
			windows.GetOverlappedResult(h, ov, &n, true)
			return errPipeClosed
		}
		err = windows.GetOverlappedResult(h, ov, &n, false)
	}
	if err == windows.ERROR_PIPE_CONNECTED {
		err = nil
	}
	return err
}

func (l *pipeListener) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		return
	}
	l.done = true
	windows.SetEvent(l.closeEv)
	if l.next != 0 {
		windows.CloseHandle(l.next)
		l.next = 0
	}
}

// dialPipe connects to a helper's pipe, waiting for it to appear.
func dialPipe(ctx context.Context, name string) (*pipeConn, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	for {
		h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING,
			windows.FILE_FLAG_OVERLAPPED|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
		if err == nil {
			c, err := newPipeConn(h)
			if err != nil {
				windows.CloseHandle(h)
			}
			return c, err
		}
		if err != windows.ERROR_FILE_NOT_FOUND && err != windows.ERROR_PIPE_BUSY {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
