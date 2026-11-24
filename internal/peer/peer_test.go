package peer_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

type echoReq struct {
	Prefix string `json:"prefix"`
}

func echoRegistry() *peer.Registry {
	reg := peer.NewRegistry()
	reg.Handle("echo", func(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
		var req echoReq
		if err := peer.DecodeReq(raw, &req); err != nil {
			return err
		}
		if _, err := s.Write([]byte(req.Prefix)); err != nil {
			return err
		}
		_, err := io.Copy(s, s)
		return err
	})
	return reg
}

func TestOpenAndEcho(t *testing.T) {
	client, _ := peertest.PairWith(t, echoRegistry())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	s, err := client.Open(ctx, "echo", echoReq{Prefix: ">"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(s)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != ">hello" {
		t.Fatalf("got %q", got)
	}
}

func TestUnknownService(t *testing.T) {
	client, _ := peertest.PairWith(t, echoRegistry())
	_, err := client.Open(context.Background(), "nope", nil)
	var re *peer.RemoteError
	if !errors.As(err, &re) || re.Message != peer.ErrUnknownService {
		t.Fatalf("want RemoteError unknown service, got %v", err)
	}
}

// A host must not be able to open host services on the client controlling it.
func TestClientExposesNothingByDefault(t *testing.T) {
	client, host := peertest.PairWith(t, echoRegistry())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := host.Open(ctx, "echo", nil)
	var re *peer.RemoteError
	if !errors.As(err, &re) {
		t.Fatalf("want RemoteError, got %v", err)
	}

	client.HandleLocal("callback", func(ctx context.Context, c *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		_, err := s.Write([]byte("pong"))
		return err
	})
	s, err := host.Open(ctx, "callback", nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(s)
	if string(got) != "pong" {
		t.Fatalf("got %q", got)
	}
}

func TestCloseEndsSession(t *testing.T) {
	client, host := peertest.PairWith(t, echoRegistry())
	client.Close()
	select {
	case <-host.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("host session did not end after client closed")
	}
	if _, err := client.Open(context.Background(), "echo", nil); err == nil {
		t.Fatal("Open on closed session succeeded")
	}
}

func TestOpenHonoursContext(t *testing.T) {
	reg := peer.NewRegistry()
	client, _ := peertest.PairWith(t, reg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Open(ctx, "echo", nil); err == nil {
		t.Fatal("Open with cancelled context succeeded")
	}
}

// The client has no reason to trust the host, so it refuses the large stream
// headers a host is allowed to receive.
func TestClientRefusesLargeHeaders(t *testing.T) {
	client, host := peertest.PairWith(t, echoRegistry())
	called := make(chan struct{}, 1)
	client.HandleLocal("callback", func(ctx context.Context, c *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		called <- struct{}{}
		return nil
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	big := echoReq{Prefix: strings.Repeat("x", 100<<10)}
	if _, err := host.Open(ctx, "callback", big); err == nil {
		t.Fatal("client accepted a 100 KiB stream header")
	}
	select {
	case <-called:
		t.Fatal("handler ran for an oversized header")
	default:
	}
	// The same header is fine in the other direction.
	s, err := client.Open(ctx, "echo", big)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
}

func TestAuthorizeRefusesServices(t *testing.T) {
	ln := peertest.Options{Authorize: func(service string) error {
		if service == "echo" {
			return errors.New("shell is turned off on this host")
		}
		return nil
	}}
	client, _ := peertest.PairOpts(t, echoRegistry(), ln)
	_, err := client.Open(context.Background(), "echo", nil)
	var re *peer.RemoteError
	if !errors.As(err, &re) || re.Message != "shell is turned off on this host" {
		t.Fatalf("want the refusal message, got %v", err)
	}
}

// Two services share the session's one datagram queue without seeing each
// other's packets.
func TestDatagramChannelsAreSeparate(t *testing.T) {
	client, host := peertest.PairWith(t, echoRegistry())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ca, err := client.Datagrams(peer.DatagramForwardUDP)
	if err != nil {
		t.Fatal(err)
	}
	cb, _ := client.Datagrams(peer.DatagramVPN)
	ha, _ := host.Datagrams(peer.DatagramForwardUDP)
	hb, _ := host.Datagrams(peer.DatagramVPN)
	if again, _ := client.Datagrams(peer.DatagramVPN); again != cb {
		t.Fatal("a second call returned a different channel")
	}
	if err := ca.Send([]byte("for-a")); err != nil {
		t.Fatal(err)
	}
	if err := cb.Send([]byte("for-b")); err != nil {
		t.Fatal(err)
	}
	if p, err := hb.Receive(ctx); err != nil || string(p) != "for-b" {
		t.Fatalf("vpn channel got %q %v", p, err)
	}
	if p, err := ha.Receive(ctx); err != nil || string(p) != "for-a" {
		t.Fatalf("forward channel got %q %v", p, err)
	}
	short, c2 := context.WithTimeout(ctx, 100*time.Millisecond)
	defer c2()
	if p, err := ha.Receive(short); err == nil {
		t.Fatalf("unexpected extra datagram %q", p)
	}
}

// A handler that cleans up after its session ended is waited for.
func TestWaitHandlersAfterClose(t *testing.T) {
	reg := peer.NewRegistry()
	started, release := make(chan struct{}), make(chan struct{})
	reg.Handle("slow", func(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
		close(started)
		<-ctx.Done()
		<-release // the cleanup a handler does once its session is gone
		return nil
	})
	client, host := peertest.PairWith(t, reg)
	if !host.WaitHandlers(time.Millisecond) {
		t.Fatal("WaitHandlers without handlers did not return true at once")
	}
	if _, err := client.Open(context.Background(), "slow", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not start")
	}
	client.Close()
	<-host.Done()
	if host.WaitHandlers(100 * time.Millisecond) {
		t.Fatal("WaitHandlers returned while a handler was still cleaning up")
	}
	close(release)
	if !host.WaitHandlers(5 * time.Second) {
		t.Fatal("WaitHandlers timed out after the handler returned")
	}
}

// A connection made with New serves nothing until Start, so whatever its
// owner attaches in between is in place for the very first stream, even
// one the remote side opened at once.
func TestNoStreamBeforeStart(t *testing.T) {
	type key struct{}
	reg := peer.NewRegistry()
	reg.Handle("probe", func(_ context.Context, c *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		v, _ := c.Value(key{})
		ready, _ := v.(string)
		_, err := s.Write([]byte(ready + "\n"))
		return err
	})
	client, host := peertest.PairOpts(t, reg, peertest.Options{NoStart: true})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got := make(chan string, 1)
	go func() {
		s, err := client.Open(ctx, "probe", nil)
		if err != nil {
			got <- "open: " + err.Error()
			return
		}
		defer s.Close()
		b, _ := io.ReadAll(s)
		got <- strings.TrimSpace(string(b))
	}()
	select {
	case g := <-got:
		t.Fatalf("stream served before Start: %q", g)
	case <-time.After(300 * time.Millisecond):
	}
	host.SetValue(key{}, "ready")
	host.Start()
	host.Start() // idempotent
	if g := <-got; g != "ready" {
		t.Fatalf("first stream saw %q, want %q", g, "ready")
	}
}
