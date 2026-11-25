package nat

import "syscall"

func unreachableErrno() syscall.Errno { return wsaenetunreach }
