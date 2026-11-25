//go:build !windows

package service

import (
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"syscall"
)

// untrustedExe says why exe could be replaced by an account other than the
// trusted ones, or returns "". A system service runs its binary with the
// service's privileges, so whoever can change that file, or rename or
// replace any directory on its path, gets them. Both the path as written
// into the service definition and the file it resolves to are checked:
// every component must belong to a trusted account and be writable by
// nobody else (group write is accepted only for group 0).
func untrustedExe(exe string, trusted map[uint32]bool) string {
	real, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return fmt.Sprintf("cannot check %s: %v", exe, err)
	}
	paths := []string{filepath.Clean(exe)}
	if real != paths[0] {
		paths = append(paths, real)
	}
	for _, p := range paths {
		for d := p; ; d = filepath.Dir(d) {
			fi, err := os.Lstat(d)
			if err != nil {
				return fmt.Sprintf("cannot check %s: %v", d, err)
			}
			// A symlink's own bits mean nothing; who may replace it is
			// decided by its directory (checked next), and its target is
			// covered by the resolved path.
			if fi.Mode()&os.ModeSymlink == 0 {
				st, ok := fi.Sys().(*syscall.Stat_t)
				if !ok {
					return "cannot read the owner of " + d
				}
				perm := fi.Mode().Perm()
				switch {
				case !trusted[st.Uid]:
					return fmt.Sprintf("%s belongs to %s", d, account(st.Uid))
				case perm&0o002 != 0, perm&0o020 != 0 && st.Gid != 0:
					return fmt.Sprintf("%s is writable by other users (mode %v)", d, perm)
				}
			}
			if parent := filepath.Dir(d); parent == d {
				break
			}
		}
	}
	return ""
}

func account(uid uint32) string {
	if u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10)); err == nil {
		return u.Username
	}
	return "uid " + strconv.FormatUint(uint64(uid), 10)
}

// serviceAccounts are the accounts a system service's binary may belong to:
// root, and the account the service runs as.
func serviceAccounts(runAs string) (map[uint32]bool, error) {
	t := map[uint32]bool{0: true}
	if runAs != "" {
		u, err := user.Lookup(runAs)
		if err != nil {
			return nil, fmt.Errorf("--run-as %s: %w", runAs, err)
		}
		id, err := strconv.ParseUint(u.Uid, 10, 32)
		if err != nil {
			return nil, err
		}
		t[uint32(id)] = true
	}
	return t, nil
}

// checkSystemExe refuses a binary that a system service would run with more
// privileges than whoever can change it has.
func checkSystemExe(exe string, o Options, fix string) error {
	trusted, err := serviceAccounts(o.RunAs)
	if err != nil {
		return err
	}
	why := untrustedExe(exe, trusted)
	if why == "" {
		return nil
	}
	as := "root"
	if o.RunAs != "" {
		as = o.RunAs
	}
	return fmt.Errorf("the service would run %s as %s, but %s: whoever can change it would get %s's rights. "+
		"Install the binary where only root can write, and install the service from there:\n%s", exe, as, why, as, fix)
}
