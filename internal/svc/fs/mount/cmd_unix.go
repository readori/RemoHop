//go:build linux || darwin

package mount

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/readori/RemoHop/internal/bg"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
)

func mountCmd(ctx context.Context, env *cli.Env, a mountArgs) int {
	// macOS has a WebDAV client built in and no FUSE without installing
	// a kernel extension, so WebDAV is its default; Linux has FUSE.
	if a.webdav || a.o.ServeOnly || (runtime.GOOS == "darwin" && !a.fuse) {
		return davCmd(ctx, env, a)
	}
	dir, err := filepath.Abs(a.local)
	if err != nil {
		env.Errorf("mount: %v", err)
		return cli.ExitUsage
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		env.Errorf("mount: %s is not an existing directory", dir)
		return cli.ExitUsage
	}
	if isMounted(dir) {
		env.Errorf("mount: %s is already a mountpoint", dir)
		return cli.ExitError
	}
	if !a.foreground && !bg.InChild() {
		msg, _, err := bg.Start(60 * time.Second)
		if err != nil {
			env.Errorf("mount: %v", err)
			return cli.ExitError
		}
		fmt.Fprintln(env.Stdout, msg)
		return cli.ExitOK
	}

	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return cli.ExitConnect
	}
	o := a.o
	o.FsName = "remohop:" + env.Target + ":" + a.remote
	o.Redial = func(ctx context.Context) (*peer.Conn, error) { return env.DialFunc(ctx, env) }
	o.Logf = func(format string, args ...any) { env.Errorf("mount: "+format, args...) }
	mt, err := New(ctx, conn, a.remote, dir, o)
	if err != nil {
		env.Errorf("mount: %v", err)
		return cli.ExitError
	}
	// The remote path is the host's realpath: sanitize it before it
	// reaches a terminal (here, `remohop ps`, or the parent of -b).
	remote := cli.Sanitize(mt.Remote)
	msg := cli.Sanitize(fmt.Sprintf("mounted %s:%s on %s (pid %d; `remohop umount %s` to stop)",
		env.Target, remote, mt.Dir, os.Getpid(), mt.Dir))
	done := bg.Ready(bg.Info{Kind: "mount", Target: env.Target, Detail: bgDetail(mt.Dir, remote)}, msg)
	defer done()
	if a.foreground {
		fmt.Fprintln(env.Stdout, msg)
	}
	select {
	case <-ctx.Done():
		if err := mt.Unmount(); err != nil {
			env.Errorf("mount: %v", err)
			return cli.ExitError
		}
	case <-mt.Done():
		if err := mt.Err(); err != nil {
			env.Errorf("mount: %v", err)
			return cli.ExitError
		}
	}
	return cli.ExitOK
}

// kill sends SIGKILL to pid and waits up to two seconds for it to go.
func kill(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	for i := 0; i < 40; i++ {
		if syscall.Kill(pid, 0) != nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("process %d did not exit", pid)
}

func umountCmd(d string, lazy bool) error {
	dir, err := filepath.Abs(d)
	if err != nil {
		return err
	}
	infos, err := bg.List()
	if err != nil {
		return err
	}
	stopped := false
	for _, in := range infos {
		if in.Kind == "mount" && strings.HasPrefix(in.Detail, dir+" <- ") {
			if err := bg.Stop(in.PID); err != nil && !errors.Is(err, bg.ErrNotFound) {
				// It should exit within a few seconds of SIGTERM in
				// any state; if it is wedged anyway, it is not
				// serving anything useful: kill it.
				if kerr := kill(in.PID); kerr != nil {
					return fmt.Errorf("%v; SIGKILL: %v", err, kerr)
				}
			}
			stopped = true
		}
	}
	if isMounted(dir) {
		// No process, a process that died, or one that could not
		// unmount a busy filesystem.
		if err := unmountDir(dir, lazy); err != nil {
			if !lazy {
				return fmt.Errorf("%v (busy? try -l)", err)
			}
			return err
		}
		return nil
	}
	if !stopped {
		return errors.New("not mounted")
	}
	return nil
}
