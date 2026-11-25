package nat

import "syscall"

// Winsock's numbers for the errors softsend_other.go lists.
const (
	wsaeaddrnotavail syscall.Errno = 10049
	wsaenetdown      syscall.Errno = 10050
	wsaenetunreach   syscall.Errno = 10051
	wsaenobufs       syscall.Errno = 10055
	wsaehostdown     syscall.Errno = 10064
	wsaehostunreach  syscall.Errno = 10065
)

func transientErrno(e syscall.Errno) bool {
	switch e {
	case wsaenetunreach, wsaehostunreach, wsaenetdown, wsaehostdown, wsaeaddrnotavail, wsaenobufs:
		return true
	}
	return false
}
