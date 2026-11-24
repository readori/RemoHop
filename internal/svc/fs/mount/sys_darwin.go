package mount

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

// renameFlags maps macFUSE renamex flags to the rfs ones.
func renameFlags(f uint32) (int, bool) {
	const swap, excl = 2, 4 // RENAME_SWAP, RENAME_EXCL
	if f&^(swap|excl) != 0 {
		return 0, false
	}
	var r int
	if f&excl != 0 {
		r |= rfs.RenameNoReplace
	}
	if f&swap != 0 {
		r |= rfs.RenameExchange
	}
	return r, true
}

// unmountDir unmounts a macFUSE mountpoint; lazy forces a busy one.
func unmountDir(dir string, lazy bool) error {
	flags := 0
	if lazy {
		flags = unix.MNT_FORCE
	}
	if err := unix.Unmount(dir, flags); err == nil {
		return nil
	}
	args := []string{dir}
	if lazy {
		args = []string{"-f", dir}
	}
	out, err := exec.Command("umount", args...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}

func devOf(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev)
	}
	return 0
}

func isDeadMount(err error) bool {
	return errors.Is(err, syscall.ENOTCONN) || errors.Is(err, syscall.ENXIO)
}
