package shell

import (
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// procStat reads a process's state letter and start time (clock ticks since
// boot) from /proc. The start time identifies a process across PID reuse:
// a job's metadata records it at spawn time, and a later process that
// happens to get the same PID has a different one.
func procStat(pid int) (state byte, start uint64, ok bool) {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, false
	}
	// "pid (comm) S ppid ...": comm may contain spaces and parentheses, so
	// split after the last ')'.
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0, false
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 || len(f[0]) != 1 {
		return 0, 0, false
	}
	start, err = strconv.ParseUint(f[19], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return f[0][0], start, true
}

// procStart returns the identity token recorded for a freshly spawned job.
func procStart(pid int) uint64 {
	_, start, _ := procStat(pid)
	return start
}

// procAlive reports whether pid is still the running process that had start
// time start (0 = unknown, PID check only). Zombies count as gone.
func procAlive(pid int, start uint64) bool {
	if pid <= 0 {
		return false
	}
	st, s, ok := procStat(pid)
	if !ok {
		return false
	}
	if st == 'Z' || st == 'X' {
		return false
	}
	return start == 0 || s == start
}

// waitNoReap blocks until process pid has exited but leaves it unreaped
// (waitid WNOWAIT): the zombie keeps its PID and process group ID reserved,
// so the group may still be signalled safely until it is reaped. It reports
// false if that is not possible.
func waitNoReap(pid int) bool {
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if err == unix.EINTR {
			continue
		}
		return err == nil
	}
}
