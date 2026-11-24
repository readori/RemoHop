package shell

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Processes are created directly with CreateProcess rather than os/exec:
// os/exec can neither attach a pseudo console (PROC_THREAD_ATTRIBUTE_
// PSEUDOCONSOLE) nor put a process into a job object before it runs. Every
// child is created suspended, assigned to its own job object with
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE and only then resumed, so everything it
// ever starts belongs to the job: killTree (TerminateJobObject) ends the
// whole tree, and closing the job when the child is reaped ends whatever is
// left, which is how Windows hosts guarantee no orphans.

// child is a started process and the job object holding its process tree.
type child struct {
	pidv    int
	process windows.Handle
	job     windows.Handle

	mu       sync.Mutex
	released bool // handles closed; nothing may be signalled any more
	reapOnce sync.Once
	code     int
}

func (c *child) pid() int { return c.pidv }

// awaitExit blocks until the process has exited. The process handle keeps
// its PID reserved until reap.
func (c *child) awaitExit() {
	windows.WaitForSingleObject(c.process, windows.INFINITE)
}

// reap waits for the process (once), records its exit code and releases the
// handles. Closing the job kills any process of the tree still running.
func (c *child) reap() (code, sig int, name string) {
	c.reapOnce.Do(func() {
		c.awaitExit()
		var ec uint32
		if windows.GetExitCodeProcess(c.process, &ec) == nil {
			c.code = int(ec)
		} else {
			c.code = -1
		}
		c.mu.Lock()
		windows.CloseHandle(c.process)
		windows.CloseHandle(c.job)
		c.released = true
		c.mu.Unlock()
	})
	return c.code, 0, ""
}

// killTree terminates every process in the job. Their exit code is 1.
func (c *child) killTree() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.released {
		windows.TerminateJobObject(c.job, 1)
	}
}

// hangup is a kill on Windows: consoles have no hangup signal.
func (c *child) hangup() { c.killTree() }

func newKillJob() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	return job, nil
}

// envBlock encodes env as a Unicode environment block.
func envBlock(env []string) ([]uint16, error) {
	if env == nil {
		env = os.Environ()
	}
	var b []uint16
	for _, kv := range env {
		u, err := windows.UTF16FromString(kv) // includes the terminating NUL
		if err != nil {
			return nil, err
		}
		b = append(b, u...)
	}
	if len(b) == 0 {
		b = append(b, 0)
	}
	return append(b, 0), nil
}

// createProcess starts cmd (its Path, Dir, Env and SysProcAttr.CmdLine, or
// Args) with the given startup information, inside a new kill-on-close job.
func createProcess(cmd *exec.Cmd, si *windows.StartupInfoEx, inherit bool, flags uint32) (*child, error) {
	if cmd.Err != nil {
		return nil, cmd.Err
	}
	line := ""
	if cmd.SysProcAttr != nil {
		line = cmd.SysProcAttr.CmdLine
	}
	if line == "" {
		line = windows.ComposeCommandLine(append([]string{cmd.Path}, cmd.Args[1:]...))
	}
	app, err := windows.UTF16PtrFromString(cmd.Path)
	if err != nil {
		return nil, err
	}
	cl, err := windows.UTF16FromString(line)
	if err != nil {
		return nil, err
	}
	env, err := envBlock(cmd.Env)
	if err != nil {
		return nil, err
	}
	var dir *uint16
	if cmd.Dir != "" {
		if dir, err = windows.UTF16PtrFromString(cmd.Dir); err != nil {
			return nil, err
		}
	}
	job, err := newKillJob()
	if err != nil {
		return nil, err
	}
	si.Cb = uint32(unsafe.Sizeof(*si))
	flags |= windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_SUSPENDED
	var pi windows.ProcessInformation
	if err := windows.CreateProcess(app, &cl[0], nil, nil, inherit, flags, &env[0], dir, &si.StartupInfo, &pi); err != nil {
		windows.CloseHandle(job)
		return nil, &os.PathError{Op: "CreateProcess", Path: cmd.Path, Err: err}
	}
	runtime.KeepAlive(env)
	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		windows.TerminateProcess(pi.Process, 1)
		windows.CloseHandle(pi.Thread)
		windows.CloseHandle(pi.Process)
		windows.CloseHandle(job)
		return nil, err
	}
	windows.ResumeThread(pi.Thread)
	windows.CloseHandle(pi.Thread)
	return &child{pidv: int(pi.ProcessId), process: pi.Process, job: job}, nil
}

// ttyEOF is what a console user types to end input: Ctrl-Z, Enter.
var ttyEOF = []byte{0x1a, '\r'}

// startPiped starts cmd with pipes for stdio and a hidden console of its
// own (so console code page changes never touch the host's console).
func startPiped(cmd *exec.Cmd) (*child, io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	sa := &windows.SecurityAttributes{InheritHandle: 1}
	sa.Length = uint32(unsafe.Sizeof(*sa))
	var h [6]windows.Handle // inR, inW, outR, outW, errR, errW
	closeHandles := func(idx ...int) {
		for _, i := range idx {
			if h[i] != 0 {
				windows.CloseHandle(h[i])
				h[i] = 0
			}
		}
	}
	fail := func(err error) (*child, io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
		closeHandles(0, 1, 2, 3, 4, 5)
		return nil, nil, nil, nil, err
	}
	for i := 0; i < 6; i += 2 {
		if err := windows.CreatePipe(&h[i], &h[i+1], sa, 0); err != nil {
			return fail(err)
		}
	}
	// Our ends must not leak into the child (or it would hold its own
	// pipes open).
	for _, i := range []int{1, 2, 4} {
		if err := windows.SetHandleInformation(h[i], windows.HANDLE_FLAG_INHERIT, 0); err != nil {
			return fail(err)
		}
	}
	// Restrict inheritance to exactly the child's three ends, so no other
	// inheritable handle of the host process leaks into it.
	inherited := []windows.Handle{h[0], h[3], h[5]}
	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return fail(err)
	}
	defer attrs.Delete()
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&inherited[0]),
		uintptr(len(inherited))*unsafe.Sizeof(inherited[0])); err != nil {
		return fail(err)
	}
	si := &windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Flags = windows.STARTF_USESTDHANDLES
	si.StdInput, si.StdOutput, si.StdErr = h[0], h[3], h[5]
	c, err := createProcess(cmd, si, true, windows.CREATE_NO_WINDOW)
	runtime.KeepAlive(inherited)
	closeHandles(0, 3, 5) // the child has its own copies
	if err != nil {
		return fail(err)
	}
	return c, newHandleFile(h[1]), newHandleFile(h[2]), newHandleFile(h[4]), nil
}

// handleFile does blocking I/O on a pipe handle with a Close that never
// waits for an in-flight read or write: it cancels them (CancelIoEx) and
// the handle is released by whichever operation finishes last. os.File
// cannot be used here because closing it waits for a pending synchronous
// read to return, which for a pipe may be never.
type handleFile struct {
	mu       sync.Mutex
	h        windows.Handle
	busy     int
	closing  bool
	released bool
}

func newHandleFile(h windows.Handle) *handleFile { return &handleFile{h: h} }

func (f *handleFile) begin() (windows.Handle, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closing {
		return 0, false
	}
	f.busy++
	return f.h, true
}

func (f *handleFile) end() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.busy--
	if f.closing && f.busy == 0 {
		f.releaseLocked()
	}
}

func (f *handleFile) releaseLocked() {
	if !f.released {
		f.released = true
		windows.CloseHandle(f.h)
	}
}

func (f *handleFile) Read(p []byte) (int, error) {
	h, ok := f.begin()
	if !ok {
		return 0, io.EOF
	}
	defer f.end()
	for {
		var n uint32
		err := windows.ReadFile(h, p, &n, nil)
		if n > 0 {
			return int(n), nil
		}
		switch {
		case err == nil:
			continue // a zero-length write on the other end
		case errors.Is(err, windows.ERROR_BROKEN_PIPE), errors.Is(err, windows.ERROR_OPERATION_ABORTED),
			errors.Is(err, windows.ERROR_HANDLE_EOF), errors.Is(err, windows.ERROR_NO_DATA):
			return 0, io.EOF
		}
		return 0, err
	}
}

func (f *handleFile) Write(p []byte) (int, error) {
	h, ok := f.begin()
	if !ok {
		return 0, os.ErrClosed
	}
	defer f.end()
	total := 0
	for total < len(p) {
		var n uint32
		if err := windows.WriteFile(h, p[total:], &n, nil); err != nil {
			return total, err
		}
		total += int(n)
	}
	return total, nil
}

// Close releases the handle now if it is idle, else cancels the pending
// operations (repeatedly for a short while, in case one was just about to
// start) and leaves the release to the last of them.
func (f *handleFile) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closing {
		return nil
	}
	f.closing = true
	if f.busy == 0 {
		f.releaseLocked()
		return nil
	}
	h := f.h
	windows.CancelIoEx(h, nil)
	go func() {
		for range 40 {
			time.Sleep(50 * time.Millisecond)
			f.mu.Lock()
			if f.released {
				f.mu.Unlock()
				return
			}
			windows.CancelIoEx(h, nil)
			f.mu.Unlock()
		}
	}()
	return nil
}

// termDrain bounds how long a session waits, after its shell exited, for
// the terminal's remaining output, both before and after closing it.
// ConPTY renders asynchronously and flushes on close, so it gets longer
// than a unix PTY.
const termDrain = 2 * time.Second

// termDev is a Windows pseudo console (ConPTY) with the pipes it talks
// through: Read gets its VT-encoded output, Write types into it.
type termDev struct {
	in, out *handleFile

	mu     sync.Mutex
	hpc    windows.Handle
	closed bool

	lastOut   atomic.Int64 // UnixNano of the last output read
	closeOnce sync.Once
}

// startTerm starts cmd attached to a new pseudo console of rows x cols.
func startTerm(cmd *exec.Cmd, rows, cols int) (*child, *termDev, error) {
	if rows <= 0 || cols <= 0 {
		rows, cols = 24, 80
	}
	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, nil, 0); err != nil {
		return nil, nil, err
	}
	if err := windows.CreatePipe(&outR, &outW, nil, 0); err != nil {
		windows.CloseHandle(inR)
		windows.CloseHandle(inW)
		return nil, nil, err
	}
	var hpc windows.Handle
	err := windows.CreatePseudoConsole(coord(rows, cols), inR, outW, 0, &hpc)
	// The pseudo console holds its own duplicates of its ends. Closing ours
	// is what lets the output pipe report EOF once the console is closed.
	windows.CloseHandle(inR)
	windows.CloseHandle(outW)
	if err != nil {
		windows.CloseHandle(inW)
		windows.CloseHandle(outR)
		return nil, nil, err
	}
	t := &termDev{in: newHandleFile(inW), out: newHandleFile(outR), hpc: hpc}
	t.lastOut.Store(time.Now().UnixNano())

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		t.shutdown()
		return nil, nil, err
	}
	defer attrs.Delete()
	// The attribute's value is the HPCON itself, not a pointer to it.
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		*(*unsafe.Pointer)(unsafe.Pointer(&hpc)), unsafe.Sizeof(hpc)); err != nil {
		t.shutdown()
		return nil, nil, err
	}
	si := &windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	// Null standard handles: the child must talk to the pseudo console, not
	// inherit the host's own (possibly redirected) stdio.
	si.Flags = windows.STARTF_USESTDHANDLES
	c, err := createProcess(cmd, si, false, 0)
	if err != nil {
		t.shutdown()
		return nil, nil, err
	}
	return c, t, nil
}

func coord(rows, cols int) windows.Coord {
	return windows.Coord{X: int16(clampDim(cols)), Y: int16(clampDim(rows))}
}

func (t *termDev) Read(p []byte) (int, error) {
	n, err := t.out.Read(p)
	if n > 0 {
		t.lastOut.Store(time.Now().UnixNano())
	}
	return n, err
}

func (t *termDev) Write(p []byte) (int, error) { return t.in.Write(p) }

func (t *termDev) resize(rows, cols int) error {
	if rows <= 0 || cols <= 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return os.ErrClosed
	}
	return windows.ResizePseudoConsole(t.hpc, coord(rows, cols))
}

// redraw is a no-op: ConPTY repaints on its own when its size changes and
// keeps no foreground process group to signal.
func (t *termDev) redraw() {}

// processExited closes the pseudo console once the client process is gone:
// unlike a unix PTY, ConPTY keeps its output pipe open until it is closed,
// and only then flushes the last frame and reports EOF. A short quiet
// period first lets it render what the process wrote just before exiting.
func (t *termDev) processExited() {
	go func() {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) &&
			time.Since(time.Unix(0, t.lastOut.Load())) < 150*time.Millisecond {
			time.Sleep(50 * time.Millisecond)
		}
		t.Close()
	}()
}

// Close shuts the pseudo console down in the background and returns at
// once; the reader sees EOF when it is done (or its read is cancelled).
func (t *termDev) Close() error {
	t.closeOnce.Do(func() { go t.shutdown() })
	return nil
}

// shutdown closes the console while the output keeps being drained by the
// reader (ClosePseudoConsole may wait for that output to be consumed), with
// every step bounded.
func (t *termDev) shutdown() {
	t.mu.Lock()
	hpc := t.hpc
	already := t.closed
	t.closed = true
	t.mu.Unlock()
	t.in.Close()
	if !already {
		done := make(chan struct{})
		go func() {
			windows.ClosePseudoConsole(hpc)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	}
	// Give the reader a moment to take the final output and EOF, then
	// cancel whatever read is still pending.
	time.Sleep(500 * time.Millisecond)
	t.out.Close()
}
