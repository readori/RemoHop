//go:build linux || darwin

package serial

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/creack/pty"

	"github.com/readori/RemoHop/internal/peer"
)

// pollInterval is how often the local slave's termios is compared with the
// last one sent. Changes are also noticed immediately before any byte the
// local program writes is forwarded, so a "set 9600, then send" sequence
// reaches the host in order.
const pollInterval = 50 * time.Millisecond

// AttachOptions configure Attach.
type AttachOptions struct {
	OpenOptions
	// LocalPath is where the virtual port appears (a symlink to the pty
	// slave). Required.
	LocalPath string
}

// Virtual is a local virtual serial port wired to a remote one.
type Virtual struct {
	Remote *Remote
	// Path is the symlink the user gave; Slave the real pty device behind it.
	Path, Slave string

	master, slave *os.File
	mu            sync.Mutex // guards last and sent
	last          Config     // line settings of the slave when last looked at
	sent          Config     // settings last sent to the host
	done          chan struct{}
	once          sync.Once
}

// Attach opens the remote port and creates the local virtual serial port.
func Attach(ctx context.Context, c *peer.Conn, port string, cfg Config, o AttachOptions) (*Virtual, error) {
	if o.LocalPath == "" {
		return nil, errors.New("serial: no local path")
	}
	if err := checkLink(o.LocalPath); err != nil {
		return nil, err
	}
	r, err := Open(ctx, c, port, cfg, o.OpenOptions)
	if err != nil {
		return nil, err
	}
	master, slave, err := pty.Open()
	if err != nil {
		r.Close()
		return nil, fmt.Errorf("create pty: %w", err)
	}
	v := &Virtual{Remote: r, Path: o.LocalPath, Slave: slave.Name(), master: master, slave: slave, sent: cfg, done: make(chan struct{})}
	fail := func(err error) (*Virtual, error) {
		master.Close()
		slave.Close()
		r.Close()
		return nil, err
	}
	os.Chmod(v.Slave, 0o600)
	// What the slave reads back is the baseline: a pty cannot hold every
	// setting (Linux forces 8N on the slave), so only differences from it
	// are ever sent on.
	if err := ctl(slave, func(fd int) error {
		if err := makeRaw(fd, cfg); err != nil {
			return err
		}
		var err error
		v.last, err = lineOf(fd)
		return err
	}); err != nil {
		return fail(fmt.Errorf("configure virtual port: %w", err))
	}
	if err := makeLink(o.LocalPath, v.Slave); err != nil {
		return fail(err)
	}
	go v.fromRemote()
	go v.fromLocal()
	go v.poll()
	go func() {
		select {
		case <-r.Done():
		case <-ctx.Done():
		case <-v.done:
		}
		v.Close()
	}()
	return v, nil
}

// checkLink refuses a path that is taken, before anything is opened.
func checkLink(path string) error {
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("%s exists and is not a symlink", path)
		}
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists and its target is alive (`remohop unserial %s`, or remove it)", path, path)
		}
	}
	return nil
}

func makeLink(path, slave string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := checkLink(path); err != nil {
		return err
	}
	os.Remove(path) // absent, or dangling (left by a killed process)
	return os.Symlink(slave, path)
}

func (v *Virtual) fromRemote() {
	buf := make([]byte, 16<<10)
	for {
		n, err := v.Remote.Read(buf)
		if n > 0 {
			if _, werr := v.master.Write(buf[:n]); werr != nil {
				v.Close()
				return
			}
		}
		if err != nil {
			v.Close()
			return
		}
	}
}

func (v *Virtual) fromLocal() {
	buf := make([]byte, 16<<10)
	for {
		n, err := v.master.Read(buf)
		if n > 0 {
			v.syncLine()
			if _, werr := v.Remote.Write(buf[:n]); werr != nil {
				v.Close()
				return
			}
		}
		if err != nil {
			select {
			case <-v.done:
			default:
				v.Close()
			}
			return
		}
	}
}

func (v *Virtual) poll() {
	t := time.NewTicker(pollInterval)
	defer t.Stop()
	for {
		select {
		case <-v.done:
			return
		case <-t.C:
			v.syncLine()
		}
	}
}

// syncLine forwards a change of the slave's line settings to the host.
func (v *Virtual) syncLine() {
	var cur Config
	err := ctl(v.slave, func(fd int) (err error) { cur, err = lineOf(fd); return })
	if err != nil || cur.Validate() != nil {
		return // closed, or a setting we cannot express (B0)
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if cur == v.last {
		return
	}
	// Carry over only the fields the local program changed, so settings a
	// pty cannot represent (7E1 asked on the command line) are not undone.
	next := v.sent
	if cur.Baud != v.last.Baud {
		next.Baud = cur.Baud
	}
	if cur.Data != v.last.Data {
		next.Data = cur.Data
	}
	if cur.Parity != v.last.Parity {
		next.Parity = cur.Parity
	}
	if cur.Stop != v.last.Stop {
		next.Stop = cur.Stop
	}
	if cur.RTSCTS != v.last.RTSCTS {
		next.RTSCTS = cur.RTSCTS
	}
	v.last = cur
	if next != v.sent {
		v.sent = next
		v.Remote.SetConfig(next)
	}
}

// Done is closed once the virtual port is gone.
func (v *Virtual) Done() <-chan struct{} { return v.done }

// Err is why the port ended (nil: closed locally or by the host cleanly).
func (v *Virtual) Err() error { return v.Remote.Err() }

// Close removes the virtual port and closes the remote one.
func (v *Virtual) Close() error {
	v.once.Do(func() {
		// Done must not be visible before the path is gone.
		if l, err := os.Readlink(v.Path); err == nil && l == v.Slave {
			os.Remove(v.Path)
		}
		close(v.done)
		v.Remote.Close()
		v.master.SetDeadline(time.Now())
		v.master.Close()
		v.slave.Close()
	})
	return nil
}

var _ io.Closer = (*Virtual)(nil)
