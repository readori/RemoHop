// Command healthcheck is the container health probe for `remohop signal`: it does a
// real QUIC/TLS handshake with the rendezvous server on the loopback address and exits
// 0 when the server answers, 1 otherwise. A scratch image has no shell, curl or nc, so
// the probe is a second static binary built next to remohop.
//
// Usage: healthcheck [ADDR]   (default 127.0.0.1:21116, or $REMOHOP_HEALTH_ADDR)
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/signal"
)

func main() {
	addr := "127.0.0.1:21116"
	if v := os.Getenv("REMOHOP_HEALTH_ADDR"); v != "" {
		addr = v
	}
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	if err := probe(addr); err != nil {
		fmt.Fprintln(os.Stderr, "unhealthy:", err)
		os.Exit(1)
	}
}

func probe(addr string) error {
	sock, err := nat.Listen("127.0.0.1:0")
	if err != nil {
		return err
	}
	defer sock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	c, err := signal.Dial(ctx, sock, addr, signal.Options{})
	if err != nil {
		return err
	}
	return c.Close()
}
