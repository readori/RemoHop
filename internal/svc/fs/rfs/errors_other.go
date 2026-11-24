//go:build !unix && !windows

package rfs

import "syscall"

func nativeErrorName(error, syscall.Errno) string { return "" }
