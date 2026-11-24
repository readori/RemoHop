package forward

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/wire"
)

// holdServer accepts connections and keeps them open until the test ends.
func holdServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { close(done); ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { <-done; c.Close() }()
		}
	}()
	return ln.Addr().String()
}

// acceptOne plays a malicious or buggy host: it opens forward.accept for id
// and returns the client's status reply and the stream.
func acceptOne(t *testing.T, host *peer.Conn, id string) (status, peer.Stream) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := host.Open(ctx, svcAccept, acceptReq{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	var st status
	if err := wire.ReadJSON(s, &st); err != nil {
		t.Fatal(err)
	}
	return st, s
}

func TestReverseConnectionCaps(t *testing.T) {
	client, host := peertest.Pair(t)
	ctx := context.Background()
	target := holdServer(t)
	_, port, _ := net.SplitHostPort(target)
	spec, _ := ParseSpec("0:" + port)

	f1, err := ListenReverse(ctx, client, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer f1.Close()
	f2, err := ListenReverse(ctx, client, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	f3, err := ListenReverse(ctx, client, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer f3.Close()

	for i := range maxReversePerForward {
		if st, _ := acceptOne(t, host, f1.id); !st.OK {
			t.Fatalf("f1 connection %d refused: %s", i, st.Error)
		}
	}
	st, s := acceptOne(t, host, f1.id)
	if st.OK || !strings.Contains(st.Error, "too many") {
		t.Fatalf("per-forward cap not enforced: %+v", st)
	}
	s.Close()
	for i := range maxReversePerSession - maxReversePerForward {
		if st, _ := acceptOne(t, host, f2.id); !st.OK {
			t.Fatalf("f2 connection %d refused: %s", i, st.Error)
		}
	}
	// The session is now at its cap, so even a fresh forward is refused.
	st, s = acceptOne(t, host, f3.id)
	if st.OK || !strings.Contains(st.Error, "session") {
		t.Fatalf("per-session cap not enforced: %+v", st)
	}
	s.Close()
	if got := f1.Stats().Active + f2.Stats().Active; got != maxReversePerSession {
		t.Fatalf("active %d", got)
	}
}

func TestRejectsNonLiteralListenAddress(t *testing.T) {
	reg := peer.NewRegistry()
	for _, addr := range []string{"localhost:80", "example.invalid:80", "127.0.0.1", "[::1]:x", ""} {
		reg.Handle(svcListen, func(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
			wire.WriteJSON(s, listenReply{OK: true, Addr: addr})
			io.Copy(io.Discard, s)
			return nil
		})
		client, _ := peertest.PairWith(t, reg)
		spec, _ := ParseSpec("0:1")
		if f, err := ListenReverse(context.Background(), client, spec); err == nil {
			f.Close()
			t.Errorf("host address %q accepted", addr)
		}
	}
}
