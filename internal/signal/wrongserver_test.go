package signal_test

import (
	"context"
	"crypto/tls"
	"strings"
	"testing"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/signal"
)

// quicServer listens on loopback with conf and returns its address.
func quicServer(t *testing.T, conf *tls.Config) string {
	t.Helper()
	sock, err := nat.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := sock.Transport().Listen(conf, &quic.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close(); sock.Close() })
	go func() {
		for {
			c, err := ln.Accept(context.Background())
			if err != nil {
				return
			}
			go func() { <-c.Context().Done() }()
		}
	}()
	return sock.LocalAddr().String()
}

func dialSignal(t *testing.T, addr string) error {
	t.Helper()
	sock, err := nat.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := signal.Dial(ctx, sock, addr, signal.Options{})
	if err == nil {
		c.Close()
	}
	return err
}

// The owner's mistake: a host's address in the rendezvous setting. (This
// also keeps signal.peerALPN equal to peer.ALPN: with another value the
// host would not be recognised.)
func TestDialHostAsRendezvousServer(t *testing.T) {
	id, err := auth.NewIdentity()
	if err != nil {
		t.Fatal(err)
	}
	conf, err := id.ServerTLS(peer.ALPN, true) // what `remohop host` presents
	if err != nil {
		t.Fatal(err)
	}
	addr := quicServer(t, conf)
	err = dialSignal(t, addr)
	if err == nil {
		t.Fatal("dialling a host as the rendezvous server succeeded")
	}
	if got := signal.CodeOf(err); got != signal.CodeIsHost {
		t.Fatalf("code = %q (%v), want %q", got, err, signal.CodeIsHost)
	}
	if !signal.IsWrongServer(err) {
		t.Fatal("IsWrongServer = false")
	}
	for _, want := range []string{addr, "is a RemoHop host, not a rendezvous server"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("message %q lacks %q", err, want)
		}
	}
}

func TestDialOtherQUICServer(t *testing.T) {
	id, _ := auth.NewIdentity()
	conf, err := id.ServerTLS("h3", false)
	if err != nil {
		t.Fatal(err)
	}
	err = dialSignal(t, quicServer(t, conf))
	if got := signal.CodeOf(err); got != signal.CodeWrongProtocol {
		t.Fatalf("code = %q (%v), want %q", got, err, signal.CodeWrongProtocol)
	}
}
