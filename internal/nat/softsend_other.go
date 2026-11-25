//go:build !windows

package nat

import "syscall"

func transientErrno(e syscall.Errno) bool {
	switch e {
	case syscall.ENETUNREACH, syscall.EHOSTUNREACH, syscall.ENETDOWN, syscall.EHOSTDOWN,
		syscall.EADDRNOTAVAIL, syscall.ENOBUFS:
		return true
	}
	return false
}
