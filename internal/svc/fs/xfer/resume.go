package xfer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

// ResumeMin is the size from which a transfer is resumable: an interrupted
// copy keeps its partial data and the next identical copy continues it.
const ResumeMin = 8 << 20

// Resumable transfers write to a partial file next to the destination
// instead of a random temporary name:
//
//	.<name>.remohop-partial-<id>
//
// where id hashes the source path, size and modification time, so a source
// that changed gets a different partial file and starts over. Before
// resuming, the partial file's content is verified against the same range
// of the source (SHA-256 on each side; nothing is transferred) and the
// transfer continues after it only if they match. The partial file is
// renamed onto the destination only when complete, so nothing partial ever
// appears under the final name, and the successful install removes every
// partial file of that destination (left over by older sources). Each side
// locks the partial file while writing it, so two concurrent transfers
// never share one (the second simply does not resume).

func partialName(base, src string, e *entry) string {
	return rfs.PartialPrefix(base) + partialKey(src, e)
}

// partialKey identifies a source for resuming: its path, size and
// modification time. Uploads hand it to the host, which names the partial
// file itself (rfs.Client.PartialPath).
func partialKey(src string, e *entry) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%d\x00%d", src, e.size, e.mtime.UnixNano())))
	return hex.EncodeToString(h[:8])
}

// hashLocal is the hex SHA-256 of n bytes of a local file at offset 0.
func hashLocal(f *os.File, n int64) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(f, 0, n)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// verifiedPrefix reports how much of a partial file of size have can be
// kept: all of it if its content matches the source's first have bytes,
// else nothing.
func verifiedPrefix(have, total int64, local func() (string, error), remote func() (string, error)) int64 {
	if have <= 0 || have > total {
		return 0
	}
	lh, err := local()
	if err != nil {
		return 0
	}
	rh, err := remote()
	if err != nil || rh != lh {
		return 0
	}
	return have
}

// cannotResume reports errors after which a transfer should simply run
// without resuming: the partial file is locked by another transfer, the
// host cannot resume, or the destination is not where the partial file is.
func cannotResume(err error) bool {
	return errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EINVAL)
}

// uploadResumable uploads through a partial file on the host. ok is false
// when resuming is impossible here and the caller should upload normally.
func uploadResumable(ctx context.Context, c *rfs.Client, sp, dp string, e *entry, opts rfs.PutOptions, prog *Progress) (ok bool, err error) {
	if info, err := c.Info(ctx); err == nil && info.OS == "windows" {
		return false, nil // no file locks there, so no partial files
	}
	// The host names the partial file; a host from before that
	// (ENOSYS) takes the name the client derives.
	key := partialKey(sp, e)
	part, err := c.PartialPath(ctx, dp, key)
	if errors.Is(err, syscall.ENOSYS) {
		dir, base := path.Split(dp)
		part, key, err = dir+partialName(base, sp, e), "", nil
	}
	if err != nil {
		if ctx.Err() == nil && cannotResume(err) {
			return false, nil
		}
		return true, err
	}
	f, err := os.Open(sp)
	if err != nil {
		return true, err
	}
	defer f.Close()
	var off int64
	if fi, err := c.Stat(ctx, part); err == nil && fi.IsRegular() {
		off = verifiedPrefix(fi.Size(), e.size,
			func() (string, error) { return hashLocal(f, fi.Size()) },
			func() (string, error) { return c.HashRange(ctx, part, 0, fi.Size()) })
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return true, err
	}
	opts.Partial, opts.PartialKey, opts.Offset = part, key, off
	prog.addResumed(off)
	sent := &Progress{}
	if _, err := c.Upload(ctx, dp, countingReader{countingReader{f, sent}, prog}, opts); err != nil {
		if ctx.Err() == nil && cannotResume(err) {
			// Take back what this attempt counted: the plain upload
			// that follows sends everything again.
			prog.addResumed(-off)
			prog.addBytes(-sent.BytesDone.Load())
			return false, nil
		}
		return true, err
	}
	return true, nil
}

// downloadResumable downloads through a partial file next to dp. ok is
// false when resuming is impossible here.
func downloadResumable(ctx context.Context, c *rfs.Client, sp, dp string, e *entry, perm fs.FileMode, mtime time.Time, prog *Progress) (ok bool, err error) {
	dir, base := filepath.Split(dp)
	if dir == "" {
		dir = "."
	}
	part := filepath.Join(dir, partialName(base, sp, e))
	// The name is predictable, so it is opened without following a
	// symlink a hostile listing might have planted there, and locked.
	f, err := openPartialLocal(part)
	if err != nil {
		if cannotResume(err) {
			return false, nil
		}
		return true, err
	}
	defer f.Close() // also on success: the rename happens while locked
	fi, err := f.Stat()
	if err != nil {
		return true, err
	}
	off := verifiedPrefix(fi.Size(), e.size,
		func() (string, error) { return hashLocal(f, fi.Size()) },
		func() (string, error) { return c.HashRange(ctx, sp, 0, fi.Size()) })
	if err := f.Truncate(off); err != nil {
		return true, err
	}
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return true, err
	}
	prog.addResumed(off)
	rd, err := c.Download(ctx, sp, off)
	if err != nil {
		return true, err
	}
	_, err = io.Copy(f, countingReader{rd, prog})
	rd.Close()
	if err != nil {
		return true, err // the partial file stays for the next attempt
	}
	if err := f.Chmod(perm); err != nil {
		return true, err
	}
	if !mtime.IsZero() {
		if err := os.Chtimes(part, mtime, mtime); err != nil {
			return true, err
		}
	}
	if err := os.Rename(part, dp); err != nil {
		return true, err
	}
	removeLocalPartials(dir, base)
	return true, nil
}

// removeLocalPartials removes the partial files of a local destination,
// except any that a running transfer holds locked.
func removeLocalPartials(dir, base string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	pre := rfs.PartialPrefix(base)
	for _, e := range ents {
		if !strings.HasPrefix(e.Name(), pre) || !e.Type().IsRegular() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		f, err := openPartialLocal(p)
		if errors.Is(err, syscall.EBUSY) {
			continue
		}
		os.Remove(p)
		if f != nil {
			f.Close()
		}
	}
}
