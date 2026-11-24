//go:build !windows

package signal

import (
	"errors"
	"syscall"
)

// addrInUse reports a bind failure because the port is taken.
func addrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}
