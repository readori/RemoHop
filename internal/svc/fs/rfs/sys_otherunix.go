//go:build unix && !linux && !darwin

package rfs

import "syscall"

func statTimes(st *syscall.Stat_t) (atime, ctime int64) { return 0, 0 }

func statFS(string) (*StatFS, error) { return nil, syscall.ENOSYS }
