//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package shell

import "golang.org/x/sys/unix"

// waitNoReap blocks until process pid has exited but leaves it unreaped,
// using a kqueue NOTE_EXIT event: the zombie keeps its PID and process group
// ID reserved, so the group may still be signalled safely until it is
// reaped. It reports false if that is not possible.
func waitNoReap(pid int) bool {
	kq, err := unix.Kqueue()
	if err != nil {
		return false
	}
	defer unix.Close(kq)
	var ev unix.Kevent_t
	unix.SetKevent(&ev, pid, unix.EVFILT_PROC, unix.EV_ADD|unix.EV_ONESHOT)
	ev.Fflags = unix.NOTE_EXIT
	if _, err := unix.Kevent(kq, []unix.Kevent_t{ev}, nil, nil); err != nil {
		// ESRCH: our unreaped child is already a zombie.
		return err == unix.ESRCH
	}
	out := make([]unix.Kevent_t, 1)
	for {
		n, err := unix.Kevent(kq, nil, out, nil)
		if err == unix.EINTR {
			continue
		}
		return err == nil && n > 0
	}
}
