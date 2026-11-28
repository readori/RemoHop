//go:build unix

package xfer

import (
	"os"
	"syscall"
	"testing"
	"time"
)

// waitUnlocked waits until no transfer holds the partial file at path
// locked (see rfs lockFile: flock, per open file, so a lock taken here only
// succeeds once the host has closed its copy). An interrupted transfer's host
// side lets go once it notices the dropped session; the test fails if that
// takes more than 10 s, rather than going on with the file still held.
func waitUnlocked(t *testing.T, path string) {
	t.Helper()
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
	t.Fatalf("%s is still locked by the interrupted transfer after 10 s", path)
}
