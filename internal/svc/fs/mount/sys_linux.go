package mount

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"

	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

// renameFlags maps Linux renameat2 flags to the rfs ones.
func renameFlags(f uint32) (int, bool) {
	const noReplace, exchange = 1, 2
	if f&^(noReplace|exchange) != 0 {
		return 0, false
	}
	var r int
	if f&noReplace != 0 {
		r |= rfs.RenameNoReplace
	}
	if f&exchange != 0 {
		r |= rfs.RenameExchange
	}
	return r, true
}

// unmountDir unmounts a FUSE mountpoint: with umount2(2) as root, through
// the fusermount helper otherwise. lazy detaches a busy mount.
func unmountDir(dir string, lazy bool) error {
	if os.Geteuid() == 0 {
		flags := 0
		if lazy {
			flags = syscall.MNT_DETACH
		}
		if err := syscall.Unmount(dir, flags); err == nil || !haveFusermount() {
			return err
		}
	}
	bin := "fusermount3"
	if _, err := exec.LookPath(bin); err != nil {
		bin = "fusermount"
	}
	args := []string{"-u"}
	if lazy {
		args = append(args, "-z")
	}
	out, err := exec.Command(bin, append(args, dir)...).CombinedOutput()
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

// isDeadMount reports the errors a mountpoint whose FUSE server is gone
// returns.
func isDeadMount(err error) bool {
	return errors.Is(err, syscall.ENOTCONN) || errors.Is(err, syscall.ECONNABORTED)
}
