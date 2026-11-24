//go:build windows

package signal

import (
	"errors"

	"golang.org/x/sys/windows"
)

// addrInUse reports a bind failure because the port is taken. Windows
// reports WSAEADDRINUSE, which syscall.EADDRINUSE (an invented value there)
// does not match.
func addrInUse(err error) bool {
	return errors.Is(err, windows.WSAEADDRINUSE) || errors.Is(err, windows.WSAEACCES)
}
