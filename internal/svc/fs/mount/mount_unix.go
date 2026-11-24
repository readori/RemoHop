//go:build linux || darwin

package mount

import (
	"context"
	"errors"
	"fmt"
	"hash/maphash"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

// cacheTimeout is how long the kernel trusts entries, attributes and
// negative lookups: short, so changes made on the host show up within
// about a second, but long enough that a `git status` or a build does not
// ask the host again for every path it touches twice.
const cacheTimeout = time.Second

// Mount is a mounted remote directory.
type Mount struct {
	// Dir is the absolute local mountpoint.
	Dir string
	// Remote is the absolute remote directory.
	Remote string

	o      Options
	m      *mountFS
	conn0  *peer.Conn // the caller's session (closed by the caller)
	server *fuse.Server
	cancel context.CancelFunc

	once     sync.Once
	done     chan struct{}
	errMu    sync.Mutex
	err      error
	unmounts sync.Once
	uerr     error
}

// New mounts the remote directory remote (a host path) on the local
// directory dir and serves it until Unmount, an external unmount, or a
// session loss that reconnecting cannot repair.
func New(ctx context.Context, conn *peer.Conn, remote, dir string, o Options) (*Mount, error) {
	if o.ReconnectFor == 0 {
		o.ReconnectFor = DefaultReconnect
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if fi, err := os.Stat(abs); err != nil {
		return nil, err
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", abs)
	}
	c, err := rfs.NewClient(ctx, conn)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*Mount, error) {
		c.Close()
		return nil, err
	}
	info, err := c.Info(ctx)
	if err != nil {
		return fail(err)
	}
	root, err := c.Realpath(ctx, remote)
	if err != nil {
		return fail(err)
	}
	rfi, err := c.Stat(ctx, root)
	if err != nil {
		return fail(err)
	}
	if !rfi.IsDir() {
		return fail(fmt.Errorf("remote %s is not a directory", root))
	}
	m := &mountFS{
		root: root, rootDev: rfi.Attr().Dev, seed: maphash.MakeSeed(),
		uid: uint32(os.Getuid()), gid: uint32(os.Getgid()),
		hostUID: info.UID, hostGID: info.GID,
		readOnly: o.ReadOnly,
	}
	m.client.Store(c)

	timeout := cacheTimeout
	fsName := o.FsName
	if fsName == "" {
		fsName = "remohop:" + root
	}
	mo := fuse.MountOptions{
		FsName:        fsName,
		Name:          "remohop",
		MaxWrite:      rfs.MaxIO,
		MaxReadAhead:  rfs.MaxIO,
		DisableXAttrs: true,
		AllowOther:    o.AllowOther,
		Debug:         o.Debug,
		// As root, mount(2) directly: there may be no fusermount
		// helper at all (containers). Others need the helper.
		DirectMount: os.Geteuid() == 0,
	}
	if o.ReadOnly {
		mo.Options = append(mo.Options, "ro")
	}
	if os.Geteuid() != 0 && !haveFusermount() {
		return fail(errors.New("mounting as a regular user needs the fusermount3 (or fusermount) helper: install fuse3, or run as root"))
	}
	server, err := fs.Mount(abs, &node{m: m}, &fs.Options{
		MountOptions:    mo,
		EntryTimeout:    &timeout,
		AttrTimeout:     &timeout,
		NegativeTimeout: &timeout,
		UID:             m.uid,
		GID:             m.gid,
		RootStableAttr:  &fs.StableAttr{Ino: m.ino(rfi.Attr(), root)},
	})
	if err != nil {
		return fail(fmt.Errorf("mount %s: %w", abs, err))
	}
	sctx, cancel := context.WithCancel(context.Background())
	mt := &Mount{Dir: abs, Remote: root, o: o, m: m, conn0: conn, server: server, cancel: cancel, done: make(chan struct{})}
	go func() {
		server.Wait()
		mt.finish()
	}()
	go mt.supervise(sctx)
	return mt, nil
}

func haveFusermount() bool {
	for _, b := range []string{"fusermount3", "fusermount"} {
		if _, err := exec.LookPath(b); err == nil {
			return true
		}
	}
	return false
}

// finish stops serving: reconnect attempts end, and the client is closed
// so every request still waiting on the host fails at once with EIO.
func (mt *Mount) finish() {
	mt.once.Do(func() {
		mt.cancel()
		c := mt.m.c()
		c.Close()
		if c.Conn() != mt.conn0 {
			c.Conn().Close() // a session we redialled ourselves
		}
		close(mt.done)
	})
}

// Done is closed once the filesystem is no longer served.
func (mt *Mount) Done() <-chan struct{} { return mt.done }

// Err reports why the mount ended on its own (nil after Unmount or an
// external unmount).
func (mt *Mount) Err() error {
	mt.errMu.Lock()
	defer mt.errMu.Unlock()
	return mt.err
}

// Unmount unmounts the filesystem, within a few seconds whatever state the
// session is in. If it is busy (a shell sitting in it, an open file) it is
// detached lazily: it disappears from the namespace now, and processes
// still using it get errors. Requests stuck on an unresponsive host are
// failed rather than waited for: go-fuse's unmount waits for every request
// in progress, and those could otherwise hold the process for a minute
// after the mountpoint is gone.
func (mt *Mount) Unmount() error {
	mt.unmounts.Do(func() {
		res := make(chan error, 1)
		go func() { res <- mt.server.Unmount() }()
		var err error
		select {
		case err = <-res:
		case <-time.After(unmountGrace):
			mt.finish()
			select {
			case err = <-res:
			case <-time.After(unmountGrace):
				err = errors.New("requests did not finish")
			}
		}
		if err != nil && isMounted(mt.Dir) {
			if lerr := unmountDir(mt.Dir, true); lerr != nil {
				mt.uerr = fmt.Errorf("unmount %s: %v (lazy: %v)", mt.Dir, err, lerr)
			}
		}
		// After a lazy detach the kernel keeps the connection until its
		// last user is gone; nothing more is served either way.
		mt.finish()
	})
	return mt.uerr
}

// unmountGrace is how long Unmount waits at each step; it keeps the whole
// of Unmount well inside the 5 s `remohop stop` allows.
const unmountGrace = 1500 * time.Millisecond

func (mt *Mount) logf(format string, args ...any) {
	if mt.o.Logf != nil {
		mt.o.Logf(format, args...)
	}
}

// supervise replaces the session when it is lost. While it is down every
// operation fails fast with EIO (files open across the loss stay broken;
// new opens work again after reconnecting). If the session cannot be
// restored within ReconnectFor the filesystem is unmounted, rather than
// leaving a dead mountpoint behind.
func (mt *Mount) supervise(ctx context.Context) {
	for {
		c := mt.m.c()
		select {
		case <-ctx.Done():
			return
		case <-c.Done():
		}
		if ctx.Err() != nil {
			return
		}
		mt.logf("session lost")
		// Drop the old session for good: a host that only resets the fs
		// stream must not make every reconnect leak a session.
		c.Conn().Close()
		nc, err := mt.reconnect(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			mt.errMu.Lock()
			mt.err = err
			mt.errMu.Unlock()
			mt.logf("%v; unmounting %s", err, mt.Dir)
			go mt.Unmount()
			return
		}
		mt.m.client.Store(nc)
		mt.logf("reconnected")
	}
}

func (mt *Mount) reconnect(ctx context.Context) (*rfs.Client, error) {
	if mt.o.Redial == nil || mt.o.ReconnectFor < 0 {
		return nil, errors.New("session lost")
	}
	deadline := time.Now().Add(mt.o.ReconnectFor)
	backoff := 500 * time.Millisecond
	var lastErr error
	for time.Now().Before(deadline) {
		dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		conn, err := mt.o.Redial(dctx)
		var c *rfs.Client
		if err == nil {
			c, err = rfs.NewClient(dctx, conn)
			if err != nil {
				conn.Close()
			}
		}
		cancel()
		if err == nil {
			return c, nil
		}
		lastErr = err
		mt.logf("reconnect failed: %v", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 15*time.Second)
	}
	return nil, fmt.Errorf("session lost and reconnecting failed for %s: %v", mt.o.ReconnectFor, lastErr)
}

// isMounted reports whether dir is a mountpoint: its device differs from
// its parent's, or it cannot even be stat'ed because its FUSE server died.
func isMounted(dir string) bool {
	fi, err := os.Stat(dir)
	if err != nil {
		var pe *os.PathError
		return errors.As(err, &pe) && isDeadMount(pe.Err)
	}
	pfi, err := os.Stat(filepath.Dir(dir))
	if err != nil {
		return false
	}
	return devOf(fi) != devOf(pfi)
}
