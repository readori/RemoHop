//go:build !windows

package shell

import (
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// child is a started process that leads its own process group (a new
// session for terminals), so it and everything it spawned can be signalled
// together.
type child struct {
	cmd *exec.Cmd

	mu sync.Mutex
	// pinned: the exit was observed without reaping (waitid WNOWAIT), so the
	// zombie still reserves the PID and the group ID.
	pinned bool
	// reaped: the PID may have been recycled; never signal it again.
	reaped   bool
	reapOnce sync.Once
}

func (c *child) pid() int { return c.cmd.Process.Pid }

// awaitExit blocks until the process has exited. Where the OS allows, it is
// left unreaped so killTree stays safe until reap.
func (c *child) awaitExit() {
	if waitNoReap(c.pid()) {
		c.mu.Lock()
		c.pinned = true
		c.mu.Unlock()
		return
	}
	c.reap()
}

// reap waits for the process (once) and returns how it ended.
func (c *child) reap() (code, sig int, name string) {
	c.reapOnce.Do(func() {
		c.mu.Lock()
		if c.pinned {
			// Already exited: Wait returns at once, and holding mu keeps
			// killTree from racing with the PID being released.
			c.cmd.Wait()
			c.reaped = true
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()
		c.cmd.Wait()
		c.mu.Lock()
		c.reaped = true
		c.mu.Unlock()
	})
	return exitInfo(c.cmd.ProcessState)
}

func (c *child) signal(f func(pid int)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.reaped {
		f(c.pid())
	}
}

// killTree SIGKILLs the whole process group.
func (c *child) killTree() { c.signal(killGroup) }

// hangup sends SIGHUP to the process group, as a closed terminal would.
func (c *child) hangup() { c.signal(hangupGroup) }

// termDrain bounds how long a session waits, after its shell exited, for
// the terminal's remaining output, both before and after closing it.
const termDrain = 500 * time.Millisecond

// ttyEOF is what a terminal user types to signal end of input.
var ttyEOF = []byte{4}

// startPiped starts cmd in its own process group with pipes for stdio.
func startPiped(cmd *exec.Cmd) (*child, io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
	setGroup(cmd)
	var files []*os.File // every pipe end, closed on failure
	fail := func(err error) (*child, io.WriteCloser, io.ReadCloser, io.ReadCloser, error) {
		for _, f := range files {
			f.Close()
		}
		return nil, nil, nil, nil, err
	}
	var ends [6]*os.File
	for i := 0; i < 6; i += 2 {
		r, w, err := os.Pipe()
		if err != nil {
			return fail(err)
		}
		ends[i], ends[i+1] = r, w
		files = append(files, r, w)
	}
	inR, inW, outR, outW, errR, errW := ends[0], ends[1], ends[2], ends[3], ends[4], ends[5]
	cmd.Stdin, cmd.Stdout, cmd.Stderr = inR, outW, errW
	if err := cmd.Start(); err != nil {
		return fail(err)
	}
	// The child has its own copies now; ours would keep the pipes open.
	inR.Close()
	outW.Close()
	errW.Close()
	return &child{cmd: cmd}, inW, outR, errR, nil
}

// termDev is the host side of a pseudo-terminal: Read gets the terminal's
// output, Write types into it.
type termDev struct {
	f *os.File
}

func (t *termDev) Read(p []byte) (int, error)  { return t.f.Read(p) }
func (t *termDev) Write(p []byte) (int, error) { return t.f.Write(p) }

// Close closes the master, which hangs the terminal up and interrupts a
// blocked Read.
func (t *termDev) Close() error { return t.f.Close() }

// processExited is called once the terminal's process has exited. A unix
// PTY needs nothing: its output drains up to EIO on its own.
func (t *termDev) processExited() {}

// startTerm starts cmd on a new pseudo-terminal of the given size, as the
// leader of a new session with the PTY as its controlling terminal.
//
// The master stays in Go's runtime poller, so Close interrupts a blocked
// Read. creack/pty's helpers call File.Fd(), which switches a file to
// blocking mode for good; a reader blocked on such a master would then
// outlive Close for as long as any process (a background job, a setsid'd
// daemon) still holds the slave. So the master is re-wrapped from a
// non-blocking duplicate, and every later ioctl goes through SyscallConn.
func startTerm(cmd *exec.Cmd, rows, cols int) (*child, *termDev, error) {
	m, tty, err := pty.Open()
	if err != nil {
		return nil, nil, err
	}
	defer tty.Close()
	ptmx, err := pollable(m)
	m.Close()
	if err != nil {
		return nil, nil, err
	}
	t := &termDev{f: ptmx}
	if err := t.resize(rows, cols); err != nil {
		ptmx.Close()
		return nil, nil, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setsid = true
	cmd.SysProcAttr.Setctty = true
	cmd.SysProcAttr.Ctty = 0 // the child's stdin, i.e. tty
	if err := cmd.Start(); err != nil {
		ptmx.Close()
		return nil, nil, err
	}
	return &child{cmd: cmd}, t, nil
}

// pollable returns a new non-blocking *os.File for f's descriptor, which
// os.NewFile registers with the runtime poller.
func pollable(f *os.File) (*os.File, error) {
	var fd int
	var err error
	cerr := withFd(f, func(raw int) {
		fd, err = unix.Dup(raw)
	})
	if cerr != nil {
		return nil, cerr
	}
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), f.Name()), nil
}

// withFd runs fn on f's descriptor without taking it out of the poller.
func withFd(f *os.File, fn func(fd int)) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	return rc.Control(func(fd uintptr) { fn(int(fd)) })
}

func (t *termDev) resize(rows, cols int) error {
	if rows <= 0 || cols <= 0 {
		rows, cols = 24, 80
	}
	ws := &unix.Winsize{Row: uint16(clampDim(rows)), Col: uint16(clampDim(cols))}
	var err error
	if cerr := withFd(t.f, func(fd int) { err = unix.IoctlSetWinsize(fd, unix.TIOCSWINSZ, ws) }); cerr != nil {
		return cerr
	}
	return err
}

// redraw asks the terminal's foreground process group to repaint by sending
// SIGWINCH. The kernel only sends it when the size actually changes, but a
// re-attaching client usually has the same size and still needs full-screen
// programs (editors, top) to redraw over the replayed output.
func (t *termDev) redraw() {
	pgrp := 0
	withFd(t.f, func(fd int) { pgrp, _ = unix.IoctlGetInt(fd, unix.TIOCGPGRP) })
	if pgrp > 0 {
		syscall.Kill(-pgrp, syscall.SIGWINCH)
	}
}
