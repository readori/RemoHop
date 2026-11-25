//go:build unix

package xfer

import (
	"os"
	"syscall"
	"time"
)

// waitUnlocked waits until no transfer holds the partial file at path
// locked (see rfs lockFile: flock, per open file, so a lock taken here only
// succeeds once the host has closed its copy). An interrupted transfer's host
// side lets go only when it notices the dropped session, which can take a
// while on a slow machine; until then installs leave the file alone.
func waitUnlocked(path string) {
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		f, err := os.Open(path)
		if err != nil {
			return
		}
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		}
		f.Close()
		if err == nil {
			return
		}
	}
}
