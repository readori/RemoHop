package service

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/readori/RemoHop/internal/cli"
)

// RunHost runs the host until ctx is cancelled and returns when it has shut
// down. args are the arguments of `remohop host` (without "host"); log receives
// what the host prints. The Windows service process calls it, because a
// service has no terminal to run `remohop host` in.
//
// The default runs the registered "host" command, so no wiring is needed; it
// is a variable so the host package or a test can substitute its own entry
// point.
var RunHost = func(ctx context.Context, args []string, log io.Writer) error {
	host := cli.Lookup("host")
	if host == nil {
		return fmt.Errorf("the host command is not linked into this binary")
	}
	env := &cli.Env{Stdout: log, Stderr: log, Stdin: eofReader{}}
	if code := host.Run(ctx, env, args); code != cli.ExitOK {
		return fmt.Errorf("host exited with status %d", code)
	}
	return nil
}

type eofReader struct{}

func (eofReader) Read([]byte) (int, error) { return 0, io.EOF }

// serviceLogPath is where the Windows service writes what the host prints.
func serviceLogPath(dir string) string { return filepath.Join(dir, "service.log") }

// windowsDataDir is the config directory of the Windows service instance.
// LocalSystem's own profile is an obscure place for key material.
func windowsDataDir() string {
	base := os.Getenv("ProgramData")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "RemoHop")
}

// setServiceEnv points the service process at its config directory: the one
// it was installed with, else one the environment names, else the default.
func setServiceEnv(dir string) (string, error) {
	if dir == "" {
		dir = os.Getenv("REMOHOP_CONFIG_DIR")
	}
	if dir == "" {
		dir = windowsDataDir()
	}
	return dir, os.Setenv("REMOHOP_CONFIG_DIR", dir)
}
