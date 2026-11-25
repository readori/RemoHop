//go:build !windows

package nat

import "syscall"

func unreachableErrno() syscall.Errno { return syscall.ENETUNREACH }
