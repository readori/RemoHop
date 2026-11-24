package mount

import (
	"context"
	"errors"
	"strings"

	"github.com/readori/RemoHop/internal/bg"
	"github.com/readori/RemoHop/internal/cli"
)

// mountCmd: Windows mounts through the built-in WebDAV redirector only.
func mountCmd(ctx context.Context, env *cli.Env, a mountArgs) int {
	if a.fuse {
		env.Errorf("mount: --fuse is not available on Windows")
		return cli.ExitUsage
	}
	return davCmd(ctx, env, a)
}

// umountCmd disconnects the drive first, while the server can still answer
// the redirector, and then ends the process: Windows has no SIGTERM, so
// the process cannot be asked to do it itself.
func umountCmd(d string, _ bool) error {
	target := strings.ToUpper(strings.TrimRight(d, `\/`))
	if len(target) != 2 || target[1] != ':' {
		return errors.New("name the drive, for example Z:")
	}
	infos, err := bg.List()
	if err != nil {
		return err
	}
	var pids []int
	for _, in := range infos {
		if in.Kind == "mount" && strings.HasPrefix(in.Detail, target+" <- ") {
			pids = append(pids, in.PID)
		}
	}
	mounted := davMounted(target)
	if len(pids) == 0 && !mounted {
		return errors.New("not mounted")
	}
	var uerr error
	if mounted && len(pids) > 0 {
		uerr = davUnmount(target, true)
	} else if mounted {
		return errors.New("that drive was not mounted by remohop")
	}
	for _, pid := range pids {
		if err := bg.Stop(pid); err != nil && !errors.Is(err, bg.ErrNotFound) && uerr == nil {
			uerr = err
		}
	}
	return uerr
}
