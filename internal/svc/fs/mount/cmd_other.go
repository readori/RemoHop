//go:build !linux && !darwin && !windows

package mount

import (
	"context"
	"fmt"
	"runtime"

	"github.com/readori/RemoHop/internal/cli"
)

// mountCmd: without a known OS client only `-o nomount` (serve WebDAV for
// any client) makes sense.
func mountCmd(ctx context.Context, env *cli.Env, a mountArgs) int {
	return davCmd(ctx, env, a)
}

func umountCmd(string, bool) error {
	return fmt.Errorf("umount is not supported on %s: stop the server with `remohop stop <PID>`", runtime.GOOS)
}
