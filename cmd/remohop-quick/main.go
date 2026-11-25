// Command remohop-quick is the double-click build of RemoHop: RemoHop.exe on Windows
// and the program inside RemoHop.app on macOS (see packaging/quick). Started
// without arguments it runs `remohop app`, the desktop app, in a native window
// (internal/webview); `--quick` runs `remohop quick`, the one-shot
// quick-support window; with other arguments it is the same program as
// remohop (the Linux build is remohop-quick).
//
// On Windows it is linked with -H windowsgui so that no console window
// appears. That is also why it is a separate build from remohop: a console
// program would flash one, and a GUI program prints nothing in a terminal.
package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/readori/RemoHop/internal/cli"

	// The same set as cmd/remohop (main_test.go checks it): the host side of
	// every feature must be in this binary for a helper to use it.
	_ "github.com/readori/RemoHop/internal/bg"
	_ "github.com/readori/RemoHop/internal/desktop"
	_ "github.com/readori/RemoHop/internal/dial"
	_ "github.com/readori/RemoHop/internal/doctor"
	_ "github.com/readori/RemoHop/internal/hostd"
	_ "github.com/readori/RemoHop/internal/mcp"
	_ "github.com/readori/RemoHop/internal/mesh"
	_ "github.com/readori/RemoHop/internal/quick"
	_ "github.com/readori/RemoHop/internal/service"
	_ "github.com/readori/RemoHop/internal/signal"
	_ "github.com/readori/RemoHop/internal/svc/clip"
	_ "github.com/readori/RemoHop/internal/svc/desktop"
	_ "github.com/readori/RemoHop/internal/svc/forward"
	_ "github.com/readori/RemoHop/internal/svc/fs"
	_ "github.com/readori/RemoHop/internal/svc/input"
	_ "github.com/readori/RemoHop/internal/svc/screenshot"
	_ "github.com/readori/RemoHop/internal/svc/serial"
	_ "github.com/readori/RemoHop/internal/svc/shell"
	_ "github.com/readori/RemoHop/internal/svc/sysinfo"
	_ "github.com/readori/RemoHop/internal/svc/usb"
	_ "github.com/readori/RemoHop/internal/svc/vpn"
	_ "github.com/readori/RemoHop/internal/svc/wake"
	_ "github.com/readori/RemoHop/internal/web"
)

// quickArgs turns the arguments of a double-click into `app`, and a lone
// --quick into `quick`. Finder on older macOS versions passes a process
// serial number (-psn_0_12345).
func quickArgs(args []string) []string {
	var out []string
	for _, a := range args {
		if !strings.HasPrefix(a, "-psn_") {
			out = append(out, a)
		}
	}
	switch {
	case len(out) == 0:
		return []string{"app"}
	case out[0] == "--quick":
		return append([]string{"quick"}, out[1:]...)
	}
	return out
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Main(ctx, quickArgs(os.Args[1:]), os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
