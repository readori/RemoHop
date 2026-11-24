//go:build linux

package serial

import "golang.org/x/sys/unix"

type tflag = uint32

// Linux speaks termios2 so that any baud rate (BOTHER) can be set and read
// back exactly. x/sys's Termios has the termios2 layout.
func ttyGet(fd int) (*unix.Termios, error) { return unix.IoctlGetTermios(fd, unix.TCGETS2) }

func ttySet(fd int, t *unix.Termios) error { return unix.IoctlSetTermios(fd, unix.TCSETS2, t) }

func ttyBaud(t *unix.Termios) int { return int(t.Ospeed) }

const exclusiveQueryable = true

func exclusiveSet(fd int) bool {
	v, err := unix.IoctlGetInt(fd, unix.TIOCGEXCL)
	return err == nil && v != 0
}

var stdBauds = map[int]uint32{
	50: unix.B50, 75: unix.B75, 110: unix.B110, 134: unix.B134, 150: unix.B150, 200: unix.B200,
	300: unix.B300, 600: unix.B600, 1200: unix.B1200, 1800: unix.B1800, 2400: unix.B2400,
	4800: unix.B4800, 9600: unix.B9600, 19200: unix.B19200, 38400: unix.B38400,
	57600: unix.B57600, 115200: unix.B115200, 230400: unix.B230400, 460800: unix.B460800,
	500000: unix.B500000, 576000: unix.B576000, 921600: unix.B921600, 1000000: unix.B1000000,
	1152000: unix.B1152000, 1500000: unix.B1500000, 2000000: unix.B2000000,
	2500000: unix.B2500000, 3000000: unix.B3000000, 3500000: unix.B3500000, 4000000: unix.B4000000,
}

func setSpeedAndStore(fd int, t *unix.Termios, baud int) error {
	code, ok := stdBauds[baud]
	if !ok {
		code = unix.BOTHER
	}
	t.Cflag = t.Cflag&^(unix.CBAUD|unix.CIBAUD) | code
	t.Ispeed, t.Ospeed = uint32(baud), uint32(baud)
	return ttySet(fd, t)
}
