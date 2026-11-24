package xfer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

// SyncOptions control Sync.
type SyncOptions struct {
	// Delete removes destination entries that do not exist in the source
	// (excluded entries are never deleted).
	Delete bool
	// DryRun only reports what would change.
	DryRun bool
	// Checksum compares content hashes (computed on each side) instead of
	// modification times for files of equal size.
	Checksum bool
	// ExactPerms copies permission bits exactly. By default group and
	// other write permission is only copied where the destination side's
	// umask allows it.
	ExactPerms bool
	// NoResume always starts files over; see ResumeMin.
	NoResume bool
	// Excludes are gitignore-style patterns; see Excluder.
	Excludes []string
	// Parallel is how many files transfer at once (DefaultParallel if 0).
	Parallel int
	// Progress, if set, receives live counters.
	Progress *Progress
}

// SyncResult summarises a Sync. Paths are relative to the roots,
// "/"-separated.
type SyncResult struct {
	DryRun      bool     `json:"dry_run"`
	Transferred []string `json:"transferred"`  // files and symlinks (re)written
	Deleted     []string `json:"deleted"`      // extraneous entries removed (--delete)
	CreatedDirs []string `json:"created_dirs"` // directories created
	Updated     []string `json:"updated"`      // mode or time fixed without a transfer
	Unchanged   int      `json:"unchanged"`
	Skipped     []string `json:"skipped,omitempty"` // special files that cannot be synced
	Bytes       int64    `json:"bytes"`
	Seconds     float64  `json:"seconds"`
	Errors      []string `json:"errors,omitempty"`
}

type syncer struct {
	ctx      context.Context
	c        *rfs.Client
	src, dst fsys
	srcRoot  string
	dstRoot  string
	o        SyncOptions
	download bool
	mask     fs.FileMode // permission bits never granted (see ExactPerms)

	mu  sync.Mutex
	res SyncResult
}

func (s *syncer) fail(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.res.Errors = append(s.res.Errors, err.Error())
}

func (s *syncer) note(list *[]string, rel string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	*list = append(*list, displayRel(rel))
}

func displayRel(rel string) string {
	if rel == "" {
		return "."
	}
	return rel
}

// Sync makes dst a mirror of the directory src, one way. Exactly one side
// is remote. Both paths name the directories themselves: `sync :/srv/app
// ./app` makes ./app hold what /srv/app holds (trailing slashes make no
// difference, unlike rsync), creating ./app if needed.
//
// A file is transferred when it is missing, differs in type or size, or has
// a different modification time (whole seconds), or with Checksum a
// different SHA-256. Modification times and permission bits are copied
// (group/other write only where the destination umask allows, unless
// ExactPerms); symlinks are recreated, never followed. With Delete, entries
// present only in dst are removed — never anything outside dst, never
// excluded entries, and nothing at all if part of src could not be read.
func Sync(ctx context.Context, c *rfs.Client, src, dst Path, o SyncOptions) (*SyncResult, error) {
	start := time.Now()
	if src.Remote == dst.Remote {
		return nil, errors.New("exactly one of source and destination must be remote (prefix it with ':')")
	}
	if o.Parallel <= 0 {
		o.Parallel = DefaultParallel
	}
	if o.Progress == nil {
		o.Progress = &Progress{}
	}
	ex, err := NewExcluder(o.Excludes)
	if err != nil {
		return nil, err
	}
	s := &syncer{ctx: ctx, c: c, o: o, srcRoot: src.Path, dstRoot: dst.Path, download: src.Remote}
	s.res.DryRun = o.DryRun
	if !o.ExactPerms {
		s.mask = destUmask(ctx, c, dst.Remote) & 0o022
	}
	if src.Remote {
		s.src, s.dst = remoteFS{c}, localFS{}
	} else {
		s.src, s.dst = localFS{}, remoteFS{c}
	}

	srcTop, err := s.src.stat(ctx, src.Path)
	if err != nil {
		return nil, err
	}
	if !srcTop.isDir() {
		return nil, fmt.Errorf("%s is not a directory (use cp for single files)", src)
	}
	dstTop, err := s.dst.stat(ctx, dst.Path)
	switch {
	case err == nil && !dstTop.isDir():
		return nil, fmt.Errorf("%s exists and is not a directory", dst)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return nil, err
	case err != nil:
		dstTop = nil
	}

	var srcItems, dstItems []item
	var srcErrs, dstErrs []error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		srcItems, srcErrs = walk(ctx, s.src, src.Path, ex, o.Parallel)
	}()
	if dstTop != nil {
		dstItems, dstErrs = walk(ctx, s.dst, dst.Path, ex, o.Parallel)
	}
	wg.Wait()
	for _, e := range append(srcErrs, dstErrs...) {
		s.fail(e)
	}
	if len(srcErrs) > 0 && o.Delete {
		s.fail(errors.New("source could not be read completely: not deleting anything"))
		o.Delete = false
	}

	p := s.plan(append([]item{{rel: "", e: srcTop}}, srcItems...), dstTop, dstItems)
	if !o.DryRun {
		s.execute(p)
	}
	s.res.Seconds = time.Since(start).Seconds()
	s.res.Bytes = o.Progress.BytesDone.Load()
	if o.DryRun {
		s.res.Bytes = p.bytes
	}
	for _, l := range []*[]string{&s.res.Transferred, &s.res.Deleted, &s.res.CreatedDirs, &s.res.Updated} {
		if *l == nil {
			*l = []string{}
		}
		sort.Strings(*l)
	}
	if len(s.res.Errors) > 0 {
		return &s.res, fmt.Errorf("%w (%d errors)", ErrPartial, len(s.res.Errors))
	}
	return &s.res, nil
}

// syncPlan is the list of changes, computed before anything is touched.
type syncPlan struct {
	clear    []item // destination entries of the wrong type, removed first
	mkdirs   []item
	files    []item
	links    []item
	chmods   []item
	mtimes   []item // checksum-equal files whose time differs
	deletes  []item // destination-only entries, deepest first
	dirFixes []item // source directories whose destination needs mode/time
	bytes    int64
}

func (s *syncer) perm(e *entry) fs.FileMode {
	p := e.mode & permBits &^ s.mask
	if s.download {
		p &^= fs.ModeSetuid | fs.ModeSetgid
	}
	return p
}

func sameSecond(a, b time.Time) bool { return a.Unix() == b.Unix() }

func (s *syncer) plan(srcItems []item, dstTop *entry, dstItems []item) *syncPlan {
	p := &syncPlan{}
	dstMap := make(map[string]*entry, len(dstItems)+1)
	if dstTop != nil {
		dstMap[""] = dstTop
	}
	for _, it := range dstItems {
		dstMap[it.rel] = it.e
	}
	srcSet := make(map[string]bool, len(srcItems))
	var cleared []string // destination dirs removed for a type change
	dirty := map[string]bool{}
	markDirty := func(rel string) {
		for rel != "" {
			rel = parentRel(rel)
			dirty[rel] = true
		}
	}

	var toHash []item
	for _, it := range srcItems {
		srcSet[it.rel] = true
		d := dstMap[it.rel]
		if d != nil && !sameType(it.e, d) {
			p.clear = append(p.clear, item{rel: it.rel, e: d})
			if d.isDir() {
				cleared = append(cleared, it.rel)
			}
			d = nil
		}
		switch {
		case it.e.isDir():
			switch {
			case d == nil:
				p.mkdirs = append(p.mkdirs, it)
				p.dirFixes = append(p.dirFixes, it)
				markDirty(it.rel)
			case s.perm(d) != s.perm(it.e) || !sameSecond(d.mtime, it.e.mtime):
				p.dirFixes = append(p.dirFixes, it)
			}
		case it.e.isRegular():
			switch {
			case d == nil || d.size != it.e.size:
				p.files = append(p.files, it)
				p.bytes += it.e.size
				markDirty(it.rel)
			case s.o.Checksum:
				toHash = append(toHash, it)
			case !sameSecond(d.mtime, it.e.mtime):
				p.files = append(p.files, it)
				p.bytes += it.e.size
				markDirty(it.rel)
			case s.perm(d) != s.perm(it.e):
				p.chmods = append(p.chmods, it)
			default:
				s.res.Unchanged++
			}
		case it.e.isSymlink():
			if d == nil || d.target != it.e.target {
				p.links = append(p.links, it)
				markDirty(it.rel)
			} else {
				s.res.Unchanged++
			}
		default:
			s.res.Skipped = append(s.res.Skipped, it.rel)
		}
	}

	// With --checksum, equal-sized files are hashed on both sides at once
	// (the remote side hashes on the host, nothing is transferred).
	var hmu sync.Mutex
	parallelDo(len(toHash), s.o.Parallel, func(i int) {
		it := toHash[i]
		d := dstMap[it.rel]
		var sh, dh string
		var serr, derr error
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			sh, serr = s.src.hash(s.ctx, relPath(s.src, s.srcRoot, it.rel))
		}()
		dh, derr = s.dst.hash(s.ctx, relPath(s.dst, s.dstRoot, it.rel))
		wg.Wait()
		hmu.Lock()
		defer hmu.Unlock()
		switch {
		case serr != nil:
			s.res.Errors = append(s.res.Errors, serr.Error())
		case derr != nil || sh != dh:
			p.files = append(p.files, it)
			p.bytes += it.e.size
			markDirty(it.rel)
		case !sameSecond(d.mtime, it.e.mtime) || s.perm(d) != s.perm(it.e):
			p.mtimes = append(p.mtimes, it)
		default:
			s.res.Unchanged++
		}
	})

	if s.o.Delete {
		underCleared := func(rel string) bool {
			for _, c := range cleared {
				if strings.HasPrefix(rel, c+"/") {
					return true
				}
			}
			return false
		}
		for i := len(dstItems) - 1; i >= 0; i-- {
			it := dstItems[i]
			if !srcSet[it.rel] && !underCleared(it.rel) {
				p.deletes = append(p.deletes, it)
				markDirty(it.rel)
			}
		}
	}
	// Directories with changes inside get their time restored afterwards.
	have := map[string]bool{}
	for _, it := range p.dirFixes {
		have[it.rel] = true
	}
	for _, it := range srcItems {
		if it.e.isDir() && dirty[it.rel] && !have[it.rel] {
			p.dirFixes = append(p.dirFixes, it)
		}
	}
	sort.Slice(p.files, func(a, b int) bool { return p.files[a].e.size > p.files[b].e.size })

	// A dry run reports the plan and stops here.
	if s.o.DryRun {
		for _, it := range p.mkdirs {
			if it.rel != "" {
				s.res.CreatedDirs = append(s.res.CreatedDirs, it.rel)
			}
		}
		for _, it := range p.files {
			s.res.Transferred = append(s.res.Transferred, it.rel)
		}
		for _, it := range p.links {
			s.res.Transferred = append(s.res.Transferred, it.rel)
		}
		for _, it := range p.deletes {
			s.res.Deleted = append(s.res.Deleted, it.rel)
		}
		for _, it := range append(p.chmods, p.mtimes...) {
			s.res.Updated = append(s.res.Updated, it.rel)
		}
	}
	return p
}

func parentRel(rel string) string {
	d := path.Dir(rel)
	if d == "." {
		return ""
	}
	return d
}

func (s *syncer) execute(p *syncPlan) {
	ctx := s.ctx
	dpath := func(rel string) string { return relPath(s.dst, s.dstRoot, rel) }

	for _, it := range p.clear {
		var err error
		if it.e.isDir() {
			err = s.dst.removeAll(ctx, dpath(it.rel))
		} else {
			err = s.dst.remove(ctx, dpath(it.rel))
		}
		if err != nil {
			s.fail(err)
		}
	}

	// Directories, parents first, one level at a time. A directory that
	// cannot be created (or turns out to be something else) blocks its
	// subtree.
	var blocked []string
	isBlocked := func(rel string) bool {
		for _, b := range blocked {
			if rel == b || strings.HasPrefix(rel, b+"/") || b == "" {
				return true
			}
		}
		return false
	}
	byDepth := map[int][]item{}
	maxDepth := 0
	for _, it := range p.mkdirs {
		d := 0
		if it.rel != "" {
			d = strings.Count(it.rel, "/") + 1
		}
		byDepth[d] = append(byDepth[d], it)
		maxDepth = max(maxDepth, d)
	}
	for d := 0; d <= maxDepth; d++ {
		var mu sync.Mutex
		parallelDo(len(byDepth[d]), s.o.Parallel, func(i int) {
			it := byDepth[d][i]
			if isBlocked(parentRel(it.rel)) && it.rel != "" {
				return
			}
			err := s.makeDir(dpath(it.rel), it)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				s.fail(err)
				blocked = append(blocked, it.rel)
				return
			}
			if it.rel != "" {
				s.res.CreatedDirs = append(s.res.CreatedDirs, it.rel)
			}
		})
	}

	prog := s.o.Progress
	prog.FilesTotal.Add(int64(len(p.files) + len(p.links)))
	prog.BytesTotal.Add(p.bytes)
	jobs := append(append([]item{}, p.files...), p.links...)
	parallelDo(len(jobs), s.o.Parallel, func(i int) {
		it := jobs[i]
		if ctx.Err() != nil || isBlocked(it.rel) {
			return
		}
		defer prog.FilesDone.Add(1)
		var err error
		if it.e.isSymlink() {
			err = replaceSymlink(ctx, s.dst, it.e.target, dpath(it.rel))
		} else {
			err = transferFile(ctx, s.c, s.download, relPath(s.src, s.srcRoot, it.rel), dpath(it.rel),
				it.e, s.perm(it.e), it.e.mtime, true, !s.o.NoResume, prog)
		}
		if err != nil {
			s.fail(err)
			return
		}
		s.note(&s.res.Transferred, it.rel)
	})
	if ctx.Err() != nil {
		s.fail(ctx.Err())
		return
	}

	for _, it := range p.chmods {
		if err := s.dst.chmod(ctx, dpath(it.rel), s.perm(it.e)); err != nil {
			s.fail(err)
			continue
		}
		s.note(&s.res.Updated, it.rel)
	}
	for _, it := range p.mtimes {
		err := s.dst.chmod(ctx, dpath(it.rel), s.perm(it.e))
		if err == nil {
			err = s.dst.chtimes(ctx, dpath(it.rel), it.e.mtime)
		}
		if err != nil {
			s.fail(err)
			continue
		}
		s.note(&s.res.Updated, it.rel)
	}

	for _, it := range p.deletes {
		var err error
		if it.e.isDir() {
			err = s.dst.rmdir(ctx, dpath(it.rel))
			if errors.Is(err, fs.ErrExist) { // ENOTEMPTY
				err = fmt.Errorf("%s: not deleting a directory that still holds excluded files", displayRel(it.rel))
			}
		} else {
			err = s.dst.remove(ctx, dpath(it.rel))
		}
		if err != nil {
			s.fail(err)
			continue
		}
		s.note(&s.res.Deleted, it.rel)
	}

	// Directory modes and times last, deepest first.
	sort.SliceStable(p.dirFixes, func(a, b int) bool { return len(p.dirFixes[a].rel) > len(p.dirFixes[b].rel) })
	for _, it := range p.dirFixes {
		if isBlocked(it.rel) {
			continue
		}
		if err := s.dst.chmod(ctx, dpath(it.rel), s.perm(it.e)); err != nil {
			s.fail(err)
		}
		if err := s.dst.chtimes(ctx, dpath(it.rel), it.e.mtime); err != nil {
			s.fail(err)
		}
	}
}

// makeDir creates one destination directory. An entry that appeared there
// since the walk is accepted only if it is a real directory.
func (s *syncer) makeDir(p string, it item) error {
	perm := it.e.mode.Perm() | 0o700
	var err error
	if it.rel == "" {
		err = s.dst.mkdirAll(s.ctx, p, perm)
	} else {
		err = s.dst.mkdir(s.ctx, p, perm)
	}
	if err == nil || !errors.Is(err, fs.ErrExist) {
		return err
	}
	de, lerr := s.dst.lstat(s.ctx, p)
	if lerr != nil {
		return lerr
	}
	if !de.isDir() {
		return fmt.Errorf("%s: exists and is not a directory", p)
	}
	return nil
}
