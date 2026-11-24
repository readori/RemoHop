//go:build linux || darwin

package serial

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// ttyPort is a serial device (or any tty) opened through termios.
type ttyPort struct {
	f *os.File
}

func ctl(f *os.File, fn func(fd int) error) error {
	rc, err := f.SyscallConn()
	if err != nil {
		return err
	}
	var ierr error
	if err := rc.Control(func(fd uintptr) { ierr = fn(int(fd)) }); err != nil {
		return err
	}
	return ierr
}

// openDevice opens path as a raw, exclusive serial port with cfg. It accepts
// any tty, which is what lets tests stand a pty in for a UART.
func openDevice(path string, cfg Config) (Port, error) {
	f, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, unwrapPath(err))
	}
	p := &ttyPort{f: f}
	err = ctl(f, func(fd int) error {
		if !isTTY(fd) {
			return errors.New("not a terminal device")
		}
		// An exclusive flag already set means another program holds the
		// port. Root bypasses the EBUSY an open would give a normal user,
		// so ask the tty itself.
		if exclusiveSet(fd) {
			return errors.New("port is busy (open exclusively by another program)")
		}
		if err := unix.IoctlSetInt(fd, unix.TIOCEXCL, 0); err != nil {
			return fmt.Errorf("TIOCEXCL: %w", err)
		}
		return makeRaw(fd, cfg)
	})
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

func unwrapPath(err error) error {
	var pe *os.PathError
	if errors.As(err, &pe) {
		return pe.Err
	}
	return err
}

func isTTY(fd int) bool {
	_, err := ttyGet(fd)
	return err == nil
}

func (p *ttyPort) Read(b []byte) (int, error) {
	n, err := p.f.Read(b)
	if err != nil && errors.Is(err, syscall.EIO) {
		err = errPortGone // pty peer closed, USB adapter unplugged
	}
	return n, err
}

var errPortGone = errors.New("serial port went away")

func (p *ttyPort) Write(b []byte) (int, error) { return p.f.Write(b) }

func (p *ttyPort) Close() error {
	p.f.SetDeadline(time.Now())
	ctl(p.f, func(fd int) error { return unix.IoctlSetInt(fd, unix.TIOCNXCL, 0) })
	return p.f.Close()
}

func (p *ttyPort) Apply(cfg Config) error {
	return ctl(p.f, func(fd int) error { return applyLine(fd, cfg) })
}

func (p *ttyPort) SetModem(dtr, rts *bool) error {
	return ctl(p.f, func(fd int) error {
		set := func(bit int, v *bool) error {
			if v == nil {
				return nil
			}
			req := uint(unix.TIOCMBIC)
			if *v {
				req = unix.TIOCMBIS
			}
			return unix.IoctlSetPointerInt(fd, req, bit)
		}
		if err := set(unix.TIOCM_DTR, dtr); err != nil {
			return err
		}
		return set(unix.TIOCM_RTS, rts)
	})
}

func (p *ttyPort) Modem() (Modem, error) {
	var m Modem
	err := ctl(p.f, func(fd int) error {
		v, err := unix.IoctlGetInt(fd, unix.TIOCMGET)
		if err != nil {
			return err
		}
		m = Modem{CTS: v&unix.TIOCM_CTS != 0, DSR: v&unix.TIOCM_DSR != 0, DCD: v&unix.TIOCM_CAR != 0, RI: v&unix.TIOCM_RNG != 0}
		return nil
	})
	return m, err
}

func (p *ttyPort) Break(d time.Duration) error {
	err := ctl(p.f, func(fd int) error { return unix.IoctlSetInt(fd, unix.TIOCSBRK, 0) })
	if err != nil {
		return err
	}
	time.Sleep(d)
	return ctl(p.f, func(fd int) error { return unix.IoctlSetInt(fd, unix.TIOCCBRK, 0) })
}

// makeRaw puts the tty in raw mode (no echo, no translation, no signals,
// CLOCAL so the line needs no carrier) and applies cfg.
func makeRaw(fd int, cfg Config) error {
	t, err := ttyGet(fd)
	if err != nil {
		return err
	}
	t.Iflag &^= tflag(unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY)
	t.Oflag &^= tflag(unix.OPOST)
	t.Lflag &^= tflag(unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN)
	t.Cflag |= tflag(unix.CREAD | unix.CLOCAL)
	t.Cc[unix.VMIN] = 1
	t.Cc[unix.VTIME] = 0
	return setLine(fd, t, cfg)
}

// applyLine changes only the line parameters of the current termios.
func applyLine(fd int, cfg Config) error {
	t, err := ttyGet(fd)
	if err != nil {
		return err
	}
	return setLine(fd, t, cfg)
}

func setLine(fd int, t *unix.Termios, cfg Config) error {
	t.Cflag &^= tflag(unix.CSIZE | unix.PARENB | unix.PARODD | unix.CSTOPB | unix.CRTSCTS)
	switch cfg.Data {
	case 5:
		t.Cflag |= tflag(unix.CS5)
	case 6:
		t.Cflag |= tflag(unix.CS6)
	case 7:
		t.Cflag |= tflag(unix.CS7)
	default:
		t.Cflag |= tflag(unix.CS8)
	}
	switch cfg.Parity {
	case 'E':
		t.Cflag |= tflag(unix.PARENB)
	case 'O':
		t.Cflag |= tflag(unix.PARENB | unix.PARODD)
	}
	if cfg.Stop == 2 {
		t.Cflag |= tflag(unix.CSTOPB)
	}
	if cfg.RTSCTS {
		t.Cflag |= tflag(unix.CRTSCTS)
	}
	return setSpeedAndStore(fd, t, cfg.Baud)
}

// lineOf reads the line parameters of a tty (what a local program set).
func lineOf(fd int) (Config, error) {
	t, err := ttyGet(fd)
	if err != nil {
		return Config{}, err
	}
	c := Config{Baud: ttyBaud(t), Parity: 'N', Stop: 1}
	switch t.Cflag & tflag(unix.CSIZE) {
	case tflag(unix.CS5):
		c.Data = 5
	case tflag(unix.CS6):
		c.Data = 6
	case tflag(unix.CS7):
		c.Data = 7
	default:
		c.Data = 8
	}
	if t.Cflag&tflag(unix.PARENB) != 0 {
		c.Parity = 'E'
		if t.Cflag&tflag(unix.PARODD) != 0 {
			c.Parity = 'O'
		}
	}
	if t.Cflag&tflag(unix.CSTOPB) != 0 {
		c.Stop = 2
	}
	c.RTSCTS = t.Cflag&tflag(unix.CRTSCTS) != 0
	return c, nil
}
