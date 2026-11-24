package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// WriteFileAtomic replaces path with data so that a reader (or a crash) sees
// either the old or the new content, never a mix: the bytes go to a
// temporary file in the same directory, are synced, and are renamed over the
// target.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	fail := func(err error) error {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Chmod(perm); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := replaceFile(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// lockWait is how long Lock waits for another process to finish.
const lockWait = 5 * time.Second

// Lock takes a cross-process advisory lock guarding path, for the
// read-modify-write cycles on small state files that several remohop processes
// share (saved peers, pairing tokens). The lock is an operating-system file
// lock (flock, LockFileEx) on path+".lock": it is released by the system if
// the holder dies, so there is nothing stale to clean up, and the lock file
// itself is never deleted, because removing a file that another process
// has open or is about to open is exactly what does not work on Windows.
// Call the returned function to release.
func Lock(path string) (unlock func(), err error) {
	deadline := time.Now().Add(lockWait)
	for {
		unlock, busy, err := tryLock(path + ".lock")
		if err == nil && !busy {
			return unlock, nil
		}
		if err != nil && !busy {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s is locked by another remohop process", path)
		}
		time.Sleep(15 * time.Millisecond)
	}
}

// replaceFile renames tmp over path. On Windows a file cannot be replaced
// while any other handle to it is open without delete sharing (somebody
// reading it at this instant, an antivirus scanner), which shows up as a
// sharing violation or "access denied"; such collisions last milliseconds,
// so the rename is retried for a while there.
func replaceFile(tmp, path string) error {
	err := os.Rename(tmp, path)
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	for wait := 5 * time.Millisecond; wait < time.Second; wait *= 2 {
		time.Sleep(wait)
		if err = os.Rename(tmp, path); err == nil {
			return nil
		}
	}
	return err
}

// ReadFile is os.ReadFile made tolerant of the transient sharing
// violations described at replaceFile: on Windows a read that races a
// WriteFileAtomic rename over the same file can fail for a moment. A file
// that does not exist is reported at once.
func ReadFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err == nil || runtime.GOOS != "windows" || errors.Is(err, fs.ErrNotExist) {
		return b, err
	}
	for wait := 5 * time.Millisecond; wait < time.Second; wait *= 2 {
		time.Sleep(wait)
		if b, err = os.ReadFile(path); err == nil || errors.Is(err, fs.ErrNotExist) {
			return b, err
		}
	}
	return b, err
}

// Rename is os.Rename made tolerant of the transient sharing violations
// described at replaceFile; use it to rotate or replace files that other
// remohop processes may be reading.
func Rename(oldpath, newpath string) error { return replaceFile(oldpath, newpath) }
