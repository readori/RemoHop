//go:build linux || darwin

package sysinfo

import "golang.org/x/sys/unix"

// statRoot fills the root filesystem sizes. Free space is what an unprivileged
// user can actually write (Bavail), not the root-reserved remainder.
func statRoot(i *Info) {
	var st unix.Statfs_t
	if err := unix.Statfs("/", &st); err != nil {
		return
	}
	// Bsize is int64 on Linux and uint32 on Darwin; widen it before multiplying.
	bsize := uint64(st.Bsize)
	i.RootTotalBytes = bsize * st.Blocks
	i.RootFreeBytes = bsize * st.Bavail
}
