package mount

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/readori/RemoHop/internal/bg"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
)

// bgDetail is the bg.Info.Detail of a mount; umount finds the process by
// its prefix.
func bgDetail(target, remote string) string { return target + " <- :" + remote }

// davCmd is `remohop <ID> mount` through the loopback WebDAV server.
func davCmd(ctx context.Context, env *cli.Env, a mountArgs) int {
	target := ""
	if !a.o.ServeOnly {
		t, err := davTarget(a.local)
		if err != nil {
			env.Errorf("mount: %v", err)
			return cli.ExitUsage
		}
		target = t
	}
	if !a.foreground && !bg.InChild() {
		msg, _, err := bg.Start(90 * time.Second)
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
	o.FsName = "remohop " + env.Target
	o.Redial = func(ctx context.Context) (*peer.Conn, error) { return env.DialFunc(ctx, env) }
	o.Logf = func(format string, args ...any) { env.Errorf("mount: "+format, args...) }
	d, err := NewWebDAV(ctx, conn, a.remote, target, o)
	if err != nil {
		env.Errorf("mount: %v", err)
		return cli.ExitError
	}
	remote := cli.Sanitize(d.Remote)
	var msg, detail string
	if o.ServeOnly {
		// The URL is the credential; it is shown to the user who asked
		// for it and kept out of `remohop ps`.
		msg = fmt.Sprintf("serving %s:%s over WebDAV at %s (pid %d; `remohop stop %d` to stop)",
			env.Target, remote, d.URL, os.Getpid(), os.Getpid())
		detail = bgDetail("webdav", remote)
	} else {
		msg = fmt.Sprintf("mounted %s:%s on %s over WebDAV (pid %d; `remohop umount %s` to stop)",
			env.Target, remote, d.Target, os.Getpid(), d.Target)
		detail = bgDetail(d.Target, remote)
	}
	msg = cli.Sanitize(msg)
	done := bg.Ready(bg.Info{Kind: "mount", Target: env.Target, Detail: detail}, msg)
	defer done()
	if a.foreground {
		fmt.Fprintln(env.Stdout, msg)
	}
	select {
	case <-ctx.Done():
		if err := d.Unmount(); err != nil {
			env.Errorf("mount: %v", err)
			return cli.ExitError
		}
	case <-d.Done():
		if err := d.Err(); err != nil {
			env.Errorf("mount: %v", err)
			return cli.ExitError
		}
	}
	return cli.ExitOK
}
