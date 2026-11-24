//go:build darwin

package serial

import (
	"unsafe"

	"golang.org/x/sys/unix"
)

type tflag = uint64

// iossiospeed is IOSSIOSPEED from <IOKit/serial/ioss.h>, the way macOS takes
// a non-standard baud rate. UNVERIFIED on real hardware.
const iossiospeed = 0x80045402

func ttyGet(fd int) (*unix.Termios, error) { return unix.IoctlGetTermios(fd, unix.TIOCGETA) }

func ttySet(fd int, t *unix.Termios) error { return unix.IoctlSetTermios(fd, unix.TIOCSETA, t) }

func ttyBaud(t *unix.Termios) int { return int(t.Ospeed) }

const exclusiveQueryable = false

func exclusiveSet(fd int) bool { return false }

// setSpeedAndStore applies baud. macOS termios speeds are plain numbers, so
// the exact rate is tried first and read back (ptys and many drivers accept
// any value); drivers that refuse or round it get IOSSIOSPEED.
func setSpeedAndStore(fd int, t *unix.Termios, baud int) error {
	t.Ispeed, t.Ospeed = uint64(baud), uint64(baud)
	if err := ttySet(fd, t); err == nil {
		if g, err := ttyGet(fd); err == nil && ttyBaud(g) == baud {
			return nil
		}
	}
	t.Ispeed, t.Ospeed = 38400, 38400
	if err := ttySet(fd, t); err != nil {
		return err
	}
	speed := uint32(baud)
	_, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), iossiospeed, uintptr(unsafe.Pointer(&speed)))
	if e != 0 {
		return e
	}
	return nil
}
