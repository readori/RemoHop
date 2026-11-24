// Command remohop is the RemoHop CLI: remote shell, FUSE mount, port forwarding,
// clipboard, file transfer and screenshots over an end-to-end encrypted P2P
// session, plus the host daemon, the rendezvous server and the MCP server.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/readori/RemoHop/internal/cli"

	// Every feature registers its commands and host services from init().
	_ "github.com/readori/RemoHop/internal/bg"
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

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Main(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
