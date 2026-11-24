package signal

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

type wsOptions struct {
	listen, path, cert, key, realIP string
	maxConns                        int
	udpListen                       net.Addr // the rendezvous socket's address
}

func checkWSFlags(listen, path, cert, key, realIP string, maxConns int) error {
	if listen == "" {
		if path != "" || cert != "" || key != "" || realIP != "" || maxConns != 0 {
			return errors.New("the --ws-* options need --ws-listen")
		}
		return nil
	}
	if _, _, err := net.SplitHostPort(listen); err != nil {
		return fmt.Errorf("--ws-listen %q: %v", listen, err)
	}
	if path != "" && (!strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?# \t\r\n")) {
		return fmt.Errorf("--ws-path %q: must be a plain path starting with /", path)
	}
	if (cert == "") != (key == "") {
		return errors.New("--ws-tls-cert and --ws-tls-key go together")
	}
	if strings.ContainsAny(realIP, " \t\r\n:") {
		return fmt.Errorf("--ws-real-ip-header %q is not a header name", realIP)
	}
	if maxConns < 0 {
		return errors.New("--ws-max-conns must be positive")
	}
	return nil
}

// bridgeTarget is where bridge sockets reach the server's UDP ports: the
// address the rendezvous socket is bound to, or loopback when it is bound
// to all addresses.
func bridgeTarget(udp net.Addr) netip.Addr {
	if ua, ok := udp.(*net.UDPAddr); ok {
		if ip, ok := netip.AddrFromSlice(ua.IP); ok && !ip.Unmap().IsUnspecified() {
			return ip.Unmap()
		}
	}
	return netip.AddrFrom4([4]byte{127, 0, 0, 1})
}

// startWS serves the websocket bridge until ctx ends or the returned stop
// is called.
func startWS(ctx context.Context, srv *Server, o wsOptions, out io.Writer, logger *slog.Logger) (stop func(), err error) {
	var certs []tls.Certificate
	if o.cert != "" {
		c, err := tls.LoadX509KeyPair(o.cert, o.key)
		if err != nil {
			return nil, fmt.Errorf("--ws-tls-cert/--ws-tls-key: %w", err)
		}
		certs = []tls.Certificate{c}
	}
	ln, err := net.Listen("tcp", o.listen)
	if err != nil {
		return nil, fmt.Errorf("--ws-listen: %w", err)
	}
	cfg := WSConfig{Path: o.path, Target: bridgeTarget(o.udpListen), MaxConns: o.maxConns, RealIPHeader: o.realIP}
	h := srv.WSHandler(cfg)
	hs := &http.Server{
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	scheme := "http"
	if certs != nil {
		scheme = "https"
		hs.TLSConfig = &tls.Config{Certificates: certs, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}}
		ln = tls.NewListener(ln, hs.TLSConfig)
	}
	go func() {
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("websocket bridge stopped", "err", err)
		}
	}()
	stopCtx := context.AfterFunc(ctx, func() { hs.Close() })
	path := o.path
	if path == "" {
		path = cfg.Path
		if path == "" {
			path = "/remohop/v1/udp"
		}
	}
	fmt.Fprintf(out, "websocket bridge: %s://%s%s (clients: remohop config set signal-ws wss://HOST%s)\n", scheme, ln.Addr(), path, path)
	if o.realIP == "" && loopbackListen(o.listen) {
		logger.Warn("websocket bridge on a loopback address without --ws-real-ip-header: behind a proxy every client " +
			"shares the proxy's address for the per-address limits; behind a Cloudflare Tunnel add --ws-real-ip-header CF-Connecting-IP")
	}
	return func() {
		stopCtx()
		hs.Close()
	}, nil
}
