package xfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

// DefaultParallel is how many files are transferred at once by default.
const DefaultParallel = 8

// smallParallel is how many requests that take one round trip each (a
// small file, a directory listing, a mkdir, a chmod) run at once, at least.
// They are bound by the round trip, not by bandwidth, and the host serves
// up to 64 requests of a session at once; files that stream keep Parallel.
// Measured at 40 ms round trip, 500 files of 4 KB took 2.8 s each way at 8
// at once and 0.76 s at 48. A variable for tests.
var smallParallel = 48

// metaParallel is how many one-round-trip requests run at once when par
// files transfer at once.
func metaParallel(par int) int { return max(par, smallParallel) }

// isSmall reports whether copying e takes one round trip: a symlink, or a
// file that goes over the control channel whole (see transferFile).
func isSmall(e *entry) bool { return !e.isRegular() || e.size <= rfs.SmallFile }

// Progress holds live counters of a transfer. All fields are updated
// atomically; read them with Load.
type Progress struct {
	FilesDone  atomic.Int64
	FilesTotal atomic.Int64
	BytesDone  atomic.Int64 // bytes actually transferred
	BytesTotal atomic.Int64
	// BytesResumed counts bytes not transferred again because an
	// interrupted transfer was resumed.
	BytesResumed atomic.Int64
}

func (p *Progress) addResumed(n int64) {
	if p != nil {
		p.BytesResumed.Add(n)
	}
}

func (p *Progress) addBytes(n int64) {
	if p != nil {
		p.BytesDone.Add(n)
	}
}

// countingReader adds every byte read to a Progress.
type countingReader struct {
	r io.Reader
	p *Progress
}

func (c countingReader) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.p.addBytes(int64(n))
	return n, err
}

// CopyOptions control Copy.
type CopyOptions struct {
	// Recursive copies directories (cp -r).
	Recursive bool
	// PreserveTimes copies modification times and exact permission bits
	// (cp -p). Without it, permission bits are copied minus the umask of
	// the destination side, as cp does. Setuid/setgid bits are only ever
	// copied on upload.
	PreserveTimes bool
	// Parallel is how many files transfer at once (DefaultParallel if 0).
	Parallel int
	// Progress, if set, receives live counters.
	Progress *Progress
	// NoResume transfers files of ResumeMin bytes or more through plain
	// temporary files, ignoring partial data of interrupted copies and
	// keeping none.
	NoResume bool
	// Stdin and Stdout back the "-" source and destination.
	Stdin  io.Reader
	Stdout io.Writer
}

// CopyResult summarises a Copy.
type CopyResult struct {
	Files    int      `json:"files"`
	Dirs     int      `json:"dirs"`
	Symlinks int      `json:"symlinks"`
	Bytes    int64    `json:"bytes"`             // transferred
	Resumed  int64    `json:"resumed,omitempty"` // kept from interrupted copies
	Seconds  float64  `json:"seconds"`
	Errors   []string `json:"errors,omitempty"`
}

// ErrPartial is returned (wrapped) when some items failed; the result lists
// them.
var ErrPartial = errors.New("some files could not be copied")

type copier struct {
	ctx  context.Context
	c    *rfs.Client
	src  fsys
	dst  fsys
	o    CopyOptions
	prog *Progress

	mu       sync.Mutex
	res      CopyResult
	dirFixes []dirFix    // directories created, fixed up after their contents
	mask     fs.FileMode // permission bits removed from copied modes
}

func (cp *copier) fail(err error) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.res.Errors = append(cp.res.Errors, err.Error())
}

func (cp *copier) count(f func(r *CopyResult)) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	f(&cp.res)
}

// job is one non-directory to copy.
type job struct {
	sp, dp   string
	e        *entry
	noFollow bool
}

// Copy copies srcs to dst with cp/scp semantics. Every source must be on
// the other side from dst (exactly one side of each pair is remote).
//
//   - If dst is an existing directory, each source is copied into it under
//     its own name; several sources require that.
//   - Otherwise the single source is copied to dst itself (a directory
//     source, with Recursive, becomes dst).
//   - Directories need Recursive. Symlinks named on the command line are
//     followed; symlinks found inside directories are recreated as
//     symlinks with the same target, never followed.
//   - Files are written to a temporary name and renamed into place.
//   - "-" as the only source reads Stdin into a remote file; "-" as dst
//     writes the remote files to Stdout.
func Copy(ctx context.Context, c *rfs.Client, srcs []Path, dst Path, o CopyOptions) (*CopyResult, error) {
	start := time.Now()
	if len(srcs) == 0 {
		return nil, errors.New("nothing to copy")
	}
	if o.Parallel <= 0 {
		o.Parallel = DefaultParallel
	}
	if o.Progress == nil {
		o.Progress = &Progress{}
	}
	for _, s := range srcs {
		if s.Remote == dst.Remote {
			if dst.Remote {
				return nil, fmt.Errorf("%s and %s are both remote: one side must be local", s, dst)
			}
			return nil, fmt.Errorf("%s and %s are both local: prefix the remote path with ':'", s, dst)
		}
	}
	cp := &copier{ctx: ctx, c: c, o: o, prog: o.Progress}
	if dst.Remote {
		cp.src, cp.dst = localFS{}, remoteFS{c}
	} else {
		cp.src, cp.dst = remoteFS{c}, localFS{}
	}
	if !o.PreserveTimes {
		cp.mask = destUmask(ctx, c, dst.Remote)
	}
	done := func() (*CopyResult, error) {
		cp.res.Seconds = time.Since(start).Seconds()
		cp.res.Bytes = cp.prog.BytesDone.Load()
		cp.res.Resumed = cp.prog.BytesResumed.Load()
		if len(cp.res.Errors) > 0 {
			return &cp.res, fmt.Errorf("%w (%d errors)", ErrPartial, len(cp.res.Errors))
		}
		return &cp.res, nil
	}

	if !dst.Remote && dst.Path == "-" {
		if err := cp.toStdout(srcs); err != nil {
			return nil, err
		}
		return done()
	}
	if len(srcs) == 1 && !srcs[0].Remote && srcs[0].Path == "-" {
		if err := cp.fromStdin(dst); err != nil {
			return nil, err
		}
		return done()
	}

	de, err := cp.dst.stat(ctx, dst.Path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	dstIsDir := err == nil && de.isDir()
	if len(srcs) > 1 && !dstIsDir {
		return nil, fmt.Errorf("target %s is not a directory", dst)
	}

	var jobs []job
	for _, s := range srcs {
		se, err := cp.src.stat(ctx, s.Path)
		if err != nil {
			cp.fail(err)
			continue
		}
		target := dst.Path
		if dstIsDir {
			name, err := cp.nameOf(s)
			if err != nil {
				cp.fail(err)
				continue
			}
			target = cp.dst.join(dst.Path, name)
		}
		switch {
		case se.isDir():
			if !o.Recursive {
				cp.fail(fmt.Errorf("%s is a directory (use -r)", s))
				continue
			}
			jobs = append(jobs, cp.planTree(s.Path, target, se)...)
		case se.isRegular():
			if !dstIsDir && strings.HasSuffix(dst.Path, "/") {
				cp.fail(fmt.Errorf("%s: no such directory", dst))
				continue
			}
			if te, err := cp.dst.stat(ctx, target); err == nil && te.isDir() {
				cp.fail(fmt.Errorf("cannot overwrite directory %s with a file", target))
				continue
			}
			jobs = append(jobs, job{sp: s.Path, dp: target, e: se})
		default:
			cp.fail(fmt.Errorf("%s: not a regular file or directory", s))
		}
	}
	cp.run(jobs)
	return done()
}

// nameOf is the name a source gets inside a destination directory.
func (cp *copier) nameOf(s Path) (string, error) {
	// A Windows host's paths may be written with backslashes, which are
	// ordinary name characters on any other host.
	slash := func(p string) string { return p }
	if s.Remote {
		if info, err := cp.c.Info(cp.ctx); err == nil && info.OS == "windows" {
			slash = func(p string) string { return strings.ReplaceAll(p, `\`, "/") }
		}
	}
	if n := baseName(Path{Remote: s.Remote, Path: slash(s.Path)}); n != "" {
		return n, nil
	}
	if s.Remote {
		// ":", ":~" or ":." name the remote home or a path without a
		// usable last component: ask the host what it really is.
		if rp, err := cp.c.Realpath(cp.ctx, s.Path); err == nil {
			if n := baseName(Path{Remote: true, Path: slash(rp)}); n != "" {
				return n, nil
			}
		}
	}
	return "", fmt.Errorf("cannot name %s inside the destination; give a destination path", s)
}

// planTree walks a source directory, creates the destination directories
// and returns the files and symlinks to copy. Directory permissions and
// times are applied after their contents by run.
func (cp *copier) planTree(srcRoot, dstRoot string, se *entry) []job {
	ctx := cp.ctx
	items, errs := walk(ctx, cp.src, srcRoot, nil, metaParallel(cp.o.Parallel))
	for _, err := range errs {
		cp.fail(err)
	}
	created, err := cp.ensureDir(dstRoot, se, true)
	if err != nil {
		cp.fail(err)
		return nil
	}
	var dirs []dirFix
	if created {
		dirs = append(dirs, dirFix{path: dstRoot, e: se, depth: 0})
	}

	// Create directories level by level: parents always exist before
	// their children, and each level's mkdirs run concurrently.
	var blocked []string // rel paths whose destination could not be made
	isBlocked := func(rel string) bool {
		for _, b := range blocked {
			if strings.HasPrefix(rel, b+"/") {
				return true
			}
		}
		return false
	}
	byDepth := map[int][]item{}
	maxDepth := 0
	for _, it := range items {
		if it.e.isDir() {
			d := strings.Count(it.rel, "/")
			byDepth[d] = append(byDepth[d], it)
			maxDepth = max(maxDepth, d)
		}
	}
	for d := 0; d <= maxDepth; d++ {
		var mu sync.Mutex
		parallelDo(len(byDepth[d]), metaParallel(cp.o.Parallel), func(i int) {
			it := byDepth[d][i]
			if isBlocked(it.rel) {
				return
			}
			p := relPath(cp.dst, dstRoot, it.rel)
			created, err := cp.ensureDir(p, it.e, false)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				cp.fail(err)
				blocked = append(blocked, it.rel)
				return
			}
			if created {
				dirs = append(dirs, dirFix{path: p, e: it.e, depth: d + 1})
			}
		})
	}
	cp.count(func(r *CopyResult) { r.Dirs += len(dirs) })
	cp.dirFixes = append(cp.dirFixes, dirs...)

	var jobs []job
	for _, it := range items {
		if it.e.isDir() || isBlocked(it.rel) {
			continue
		}
		if !it.e.isRegular() && !it.e.isSymlink() {
			cp.fail(fmt.Errorf("%s: skipping special file", relPath(cp.src, srcRoot, it.rel)))
			continue
		}
		jobs = append(jobs, job{
			sp: relPath(cp.src, srcRoot, it.rel), dp: relPath(cp.dst, dstRoot, it.rel),
			e: it.e, noFollow: true,
		})
	}
	return jobs
}

// dirFix is a directory whose final mode and time are set after its
// contents are written. depth is how far below the top of its tree it is.
type dirFix struct {
	path  string
	e     *entry
	depth int
}

// ensureDir creates a destination directory, or accepts an existing one
// (created reports which). Below the top level an existing symlink is
// refused instead of followed: otherwise a hostile listing could steer
// writes through a link.
func (cp *copier) ensureDir(p string, e *entry, top bool) (created bool, err error) {
	perm := e.mode.Perm() | 0o700 // writable until its contents are in
	err = cp.dst.mkdir(cp.ctx, p, perm)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return false, err
	}
	stat := cp.dst.lstat
	if top {
		stat = cp.dst.stat
	}
	de, err := stat(cp.ctx, p)
	if err != nil {
		return false, err
	}
	if !de.isDir() {
		if de.isSymlink() {
			return false, fmt.Errorf("%s: refusing to write through a symlink", p)
		}
		return false, fmt.Errorf("%s: exists and is not a directory", p)
	}
	return false, nil
}

// filePerm is the permission a copied file gets. Files from a remote host
// never get setuid/setgid bits locally.
func (cp *copier) filePerm(e *entry) fs.FileMode {
	perm := e.mode & permBits &^ cp.mask
	if cp.src.remote() {
		perm &^= fs.ModeSetuid | fs.ModeSetgid
	}
	return perm
}

// destUmask is the file creation mask of the side files are written to:
// this process's, or the one the host reports.
func destUmask(ctx context.Context, c *rfs.Client, remote bool) fs.FileMode {
	if !remote {
		return rfs.Umask()
	}
	if info, err := c.Info(ctx); err == nil {
		return fs.FileMode(info.Umask) & fs.ModePerm
	}
	return 0o022
}

func (cp *copier) run(jobs []job) {
	for _, j := range jobs {
		if j.e.isRegular() {
			cp.prog.BytesTotal.Add(j.e.size)
		}
	}
	cp.prog.FilesTotal.Add(int64(len(jobs)))
	// Big files first, so one large file does not start last and run
	// alone at the end.
	sort.SliceStable(jobs, func(a, b int) bool { return jobs[a].e.size > jobs[b].e.size })
	parallelBySize(jobs, func(j job) bool { return isSmall(j.e) }, cp.o.Parallel, func(j job) {
		if cp.ctx.Err() != nil {
			return
		}
		var err error
		if j.e.isSymlink() {
			err = replaceSymlink(cp.ctx, cp.dst, j.e.target, j.dp)
			if err == nil {
				cp.count(func(r *CopyResult) { r.Symlinks++ })
			}
		} else {
			var mt time.Time
			if cp.o.PreserveTimes {
				mt = j.e.mtime
			}
			err = transferFile(cp.ctx, cp.c, cp.src.remote(), j.sp, j.dp, j.e, cp.filePerm(j.e), mt, j.noFollow, !cp.o.NoResume, cp.prog)
			if err == nil {
				cp.count(func(r *CopyResult) { r.Files++ })
			}
		}
		cp.prog.FilesDone.Add(1)
		if err != nil {
			cp.fail(err)
		}
	})
	if cp.ctx.Err() != nil {
		cp.fail(cp.ctx.Err())
	}
	// Directory modes and times last, deepest first, so a read-only
	// directory is only locked once its contents are in.
	deepestFirst(cp.dirFixes, func(d dirFix) int { return d.depth }, metaParallel(cp.o.Parallel), func(d dirFix) {
		if err := cp.dst.chmod(cp.ctx, d.path, cp.filePerm(d.e)); err != nil {
			cp.fail(err)
		}
		if cp.o.PreserveTimes {
			if err := cp.dst.chtimes(cp.ctx, d.path, d.e.mtime); err != nil {
				cp.fail(err)
			}
		}
	})
}

// replaceSymlink creates a symlink at p, replacing an existing
// non-directory.
func replaceSymlink(ctx context.Context, f fsys, target, p string) error {
	err := f.symlink(ctx, target, p)
	if err == nil || !errors.Is(err, fs.ErrExist) {
		return err
	}
	if de, lerr := f.lstat(ctx, p); lerr == nil && !de.isDir() {
		if err := f.remove(ctx, p); err != nil {
			return err
		}
		return f.symlink(ctx, target, p)
	}
	return err
}

// transferFile copies one regular file across the session. Small files go
// over the control channel in one round trip; large ones stream. Both ends
// install the file atomically. noFollow replaces a symlink at dp instead of
// writing through it.
func transferFile(ctx context.Context, c *rfs.Client, download bool, sp, dp string, e *entry,
	perm fs.FileMode, mtime time.Time, noFollow, resume bool, prog *Progress) error {
	resume = resume && e.size >= ResumeMin
	if download {
		if !noFollow {
			if rp, err := filepath.EvalSymlinks(dp); err == nil {
				dp = rp
			}
		}
		if resume {
			if ok, err := downloadResumable(ctx, c, sp, dp, e, perm, mtime, prog); ok {
				return err
			}
		}
		err := writeLocalAtomic(dp, perm, mtime, func(w io.Writer) error {
			if e.size <= rfs.SmallFile {
				// One round trip if it is still small; a file that
				// grew (or a host that lied in the listing) streams
				// to disk below instead of being buffered.
				b, ok, err := c.ReadSmall(ctx, sp)
				if err != nil {
					return err
				}
				if ok {
					_, err = w.Write(b)
					prog.addBytes(int64(len(b)))
					return err
				}
			}
			rd, err := c.Download(ctx, sp, 0)
			if err != nil {
				return err
			}
			defer rd.Close()
			_, err = io.Copy(w, countingReader{rd, prog})
			return err
		})
		if err == nil && e.size >= ResumeMin {
			removeLocalPartials(filepath.Split(dp)) // leftovers of interrupted copies
		}
		return err
	}

	opts := rfs.PutOptions{Mode: perm, ModTime: mtime, NoFollow: noFollow}
	if resume {
		if ok, err := uploadResumable(ctx, c, sp, dp, e, opts, prog); ok {
			if err != nil {
				return err
			}
			return fixZeroPerm(ctx, c, dp, perm)
		}
	}
	f, err := os.Open(sp)
	if err != nil {
		return err
	}
	defer f.Close()
	var r io.Reader = f
	if e.size <= rfs.SmallFile {
		b, err := io.ReadAll(io.LimitReader(f, rfs.SmallFile+1))
		if err != nil {
			return err
		}
		if len(b) <= rfs.SmallFile {
			if _, err := c.Put(ctx, dp, b, opts); err != nil {
				return err
			}
			prog.addBytes(int64(len(b)))
			return fixZeroPerm(ctx, c, dp, perm)
		}
		r = io.MultiReader(bytes.NewReader(b), f) // it grew meanwhile
	}
	if _, err := c.Upload(ctx, dp, countingReader{r, prog}, opts); err != nil {
		return err
	}
	return fixZeroPerm(ctx, c, dp, perm)
}

// fixZeroPerm applies mode 0000, which PutOptions cannot express (zero
// means "default").
func fixZeroPerm(ctx context.Context, c *rfs.Client, p string, perm fs.FileMode) error {
	if perm != 0 {
		return nil
	}
	return c.Chmod(ctx, p, 0)
}

func (cp *copier) toStdout(srcs []Path) error {
	if cp.o.Stdout == nil {
		return errors.New("no stdout for \"-\"")
	}
	for _, s := range srcs {
		rd, err := cp.c.Download(cp.ctx, s.Path, 0)
		if err != nil {
			cp.fail(err)
			continue
		}
		_, err = io.Copy(cp.o.Stdout, countingReader{rd, cp.prog})
		rd.Close()
		if err != nil {
			cp.fail(err)
			continue
		}
		cp.count(func(r *CopyResult) { r.Files++ })
	}
	return nil
}

func (cp *copier) fromStdin(dst Path) error {
	if cp.o.Stdin == nil {
		return errors.New("no stdin for \"-\"")
	}
	if de, err := cp.dst.stat(cp.ctx, dst.Path); err == nil && de.isDir() {
		return fmt.Errorf("%s is a directory: name the file to write stdin to", dst)
	}
	if _, err := cp.c.Upload(cp.ctx, dst.Path, countingReader{cp.o.Stdin, cp.prog}, rfs.PutOptions{}); err != nil {
		cp.fail(err)
		return nil
	}
	cp.count(func(r *CopyResult) { r.Files++ })
	return nil
}

// parallelBySize runs fn on every job: the small ones (see isSmall) up to
// metaParallel(par) at a time, alongside the others, par at a time. Jobs
// keep their order within each group.
func parallelBySize[T any](jobs []T, small func(T) bool, par int, fn func(T)) {
	var big, little []T
	for _, j := range jobs {
		if small(j) {
			little = append(little, j)
		} else {
			big = append(big, j)
		}
	}
	var wg sync.WaitGroup
	wg.Go(func() { parallelDo(len(little), metaParallel(par), func(i int) { fn(little[i]) }) })
	parallelDo(len(big), par, func(i int) { fn(big[i]) })
	wg.Wait()
}

// deepestFirst runs fn on every item one depth level at a time, deepest
// first, each level's items up to par at a time: an item is only reached
// once everything deeper is done. Directory fixes and deletes go this way,
// so that a directory is locked (made read-only) or removed after what is
// in it, and a tree of many directories still costs a round trip per level
// rather than one per directory. Measured at 40 ms round trip, copying 315
// directories took 20.5 s one at a time, 3.1 s by levels.
func deepestFirst[T any](items []T, depth func(T) int, par int, fn func(T)) {
	sorted := slices.Clone(items)
	sort.SliceStable(sorted, func(a, b int) bool { return depth(sorted[a]) > depth(sorted[b]) })
	for i := 0; i < len(sorted); {
		j := i + 1
		for j < len(sorted) && depth(sorted[j]) == depth(sorted[i]) {
			j++
		}
		level := sorted[i:j]
		parallelDo(len(level), par, func(k int) { fn(level[k]) })
		i = j
	}
}

// parallelDo runs fn(0..n-1) on up to par goroutines.
func parallelDo(n, par int, fn func(i int)) {
	if n == 0 {
		return
	}
	par = min(max(par, 1), n)
	next := atomic.Int64{}
	var wg sync.WaitGroup
	for w := 0; w < par; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				i := int(next.Add(1) - 1)
				if i >= n {
					return
				}
				fn(i)
			}
		}()
	}
	wg.Wait()
}
