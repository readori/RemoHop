package forward_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/svc/forward"
	"syscall"
)

func TestParseSpec(t *testing.T) {
	tests := []struct {
		in      string
		want    forward.Spec
		wantErr bool
	}{
		{"18080:8080", forward.Spec{BindHost: "127.0.0.1", BindPort: 18080, Host: "127.0.0.1", Port: 8080}, false},
		{"0:80", forward.Spec{BindHost: "127.0.0.1", BindPort: 0, Host: "127.0.0.1", Port: 80}, false},
		{"5433:db.internal:5432", forward.Spec{BindHost: "127.0.0.1", BindPort: 5433, Host: "db.internal", Port: 5432}, false},
		{"0.0.0.0:80:10.0.0.5:8080", forward.Spec{BindHost: "0.0.0.0", BindPort: 80, Host: "10.0.0.5", Port: 8080}, false},
		{"*:80:h:81", forward.Spec{BindHost: "", BindPort: 80, Host: "h", Port: 81}, false},
		{":80:h:81", forward.Spec{BindHost: "", BindPort: 80, Host: "h", Port: 81}, false},
		{"[::1]:80:[fe80::1]:8080", forward.Spec{BindHost: "::1", BindPort: 80, Host: "fe80::1", Port: 8080}, false},
		{"80:[::1]:8080", forward.Spec{BindHost: "127.0.0.1", BindPort: 80, Host: "::1", Port: 8080}, false},
		{"", forward.Spec{}, true},
		{"80", forward.Spec{}, true},
		{"a:80", forward.Spec{}, true},
		{"80:0", forward.Spec{}, true},
		{"80:70000", forward.Spec{}, true},
		{"-1:80", forward.Spec{}, true},
		{"80::81", forward.Spec{}, true},
		{"1:2:3:4:5", forward.Spec{}, true},
		{"[::1:80:h:81", forward.Spec{}, true},
		{"80:h]:81", forward.Spec{}, true},
		{"80:a b:81", forward.Spec{}, true},
	}
	for _, tc := range tests {
		got, err := forward.ParseSpec(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseSpec(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
			continue
		}
		if err == nil && got != tc.want {
			t.Errorf("ParseSpec(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
	sp, _ := forward.ParseSpec("[::1]:80:[fe80::1]:8080")
	if sp.String() != "[::1]:80 -> [fe80::1]:8080" {
		t.Errorf("String = %q", sp.String())
	}
}

func ctxT(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// tcpServer runs handle for every accepted connection.
func tcpServer(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() { ln.Close(); wg.Wait() })
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() { defer wg.Done(); defer c.Close(); handle(c) }()
		}
	}()
	return ln.Addr().String()
}

// echoServer echoes until the client half-closes, then half-closes back.
func echoServer(t *testing.T) string {
	return tcpServer(t, func(c net.Conn) {
		io.Copy(c, c)
		c.(*net.TCPConn).CloseWrite()
	})
}

func closedPort(t *testing.T) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	a := ln.Addr().String()
	ln.Close()
	return a
}

func mustSpec(t *testing.T, s string) forward.Spec {
	t.Helper()
	sp, err := forward.ParseSpec(s)
	if err != nil {
		t.Fatal(err)
	}
	return sp
}

func specFor(t *testing.T, bindPort int, target string) forward.Spec {
	t.Helper()
	_, p, _ := net.SplitHostPort(target)
	return mustSpec(t, fmt.Sprintf("%d:%s", bindPort, p))
}

func TestLocalHTTP(t *testing.T) {
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello from the intranet")
	}))
	defer srv.Close()

	f, err := forward.Listen(ctx, client, specFor(t, 0, srv.Listener.Addr().String()))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	resp, err := http.Get("http://" + f.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "hello from the intranet" {
		t.Fatalf("body %q", body)
	}
	// The counters are bumped just after each chunk is written, so the
	// response can be back before the request's bytes are counted.
	deadline := time.Now().Add(5 * time.Second)
	for {
		st := f.Stats()
		if st.Connections == 1 && st.BytesUp > 0 && st.BytesDown > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("stats %+v", st)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDialAsNetConn(t *testing.T) {
	client, _ := peertest.Pair(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "via Dial")
	}))
	defer srv.Close()
	hc := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return forward.Dial(ctx, client, network, addr)
		},
	}}
	resp, err := hc.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "via Dial" {
		t.Fatalf("got %q", b)
	}
}

func TestLargePayloadBothWays(t *testing.T) {
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)
	addr := echoServer(t)
	f, err := forward.Listen(ctx, client, specFor(t, 0, addr))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	const size = 24 << 20
	payload := make([]byte, size)
	rand.Read(payload)
	want := sha256.Sum256(payload)

	c, err := net.Dial("tcp", f.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	go func() {
		c.Write(payload)
		c.(*net.TCPConn).CloseWrite()
	}()
	h := sha256.New()
	n, err := io.Copy(h, c)
	if err != nil || n != size {
		t.Fatalf("read back %d bytes, err %v", n, err)
	}
	var got [32]byte
	copy(got[:], h.Sum(nil))
	if got != want {
		t.Fatal("payload corrupted")
	}
}

func TestHalfClose(t *testing.T) {
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)
	// The server only answers after it has seen EOF on its read side.
	addr := tcpServer(t, func(c net.Conn) {
		n, _ := io.Copy(io.Discard, c)
		fmt.Fprintf(c, "got %d bytes", n)
	})
	f, err := forward.Listen(ctx, client, specFor(t, 0, addr))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	c, err := net.Dial("tcp", f.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write(bytes.Repeat([]byte("x"), 12345))
	c.(*net.TCPConn).CloseWrite()
	b, err := io.ReadAll(c)
	if err != nil || string(b) != "got 12345 bytes" {
		t.Fatalf("reply %q err %v", b, err)
	}

	// Same through the Dial API (net.Conn with CloseWrite).
	nc, err := forward.Dial(ctx, client, "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	nc.Write([]byte("abc"))
	nc.(interface{ CloseWrite() error }).CloseWrite()
	b, err = io.ReadAll(nc)
	if err != nil || string(b) != "got 3 bytes" {
		t.Fatalf("dial reply %q err %v", b, err)
	}
}

func TestManyConcurrentConnections(t *testing.T) {
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)
	addr := echoServer(t)
	f, err := forward.Listen(ctx, client, specFor(t, 0, addr))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	const n = 150
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.Dial("tcp", f.Addr().String())
			if err != nil {
				errs <- err
				return
			}
			defer c.Close()
			msg := bytes.Repeat([]byte(fmt.Sprintf("conn-%d;", i)), 2000)
			go func() { c.Write(msg); c.(*net.TCPConn).CloseWrite() }()
			got, err := io.ReadAll(c)
			if err != nil || !bytes.Equal(got, msg) {
				errs <- fmt.Errorf("conn %d: mismatch (err %v, %d/%d bytes)", i, err, len(got), len(msg))
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if st := f.Stats(); st.Connections != n {
		t.Fatalf("stats %+v", st)
	}
}

func TestRefusedTarget(t *testing.T) {
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)
	dead := closedPort(t)

	_, err := forward.Dial(ctx, client, "tcp", dead)
	if err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("Dial err = %v, want connection refused", err)
	}
	if _, err := forward.Dial(ctx, client, "udp", dead); err == nil {
		t.Fatal("udp dial should be rejected")
	}
	if _, err := forward.Dial(ctx, client, "tcp", "no-port"); err == nil {
		t.Fatal("malformed address should fail")
	}

	f, err := forward.Listen(ctx, client, specFor(t, 0, dead))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c, err := net.Dial("tcp", f.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("read from a connection to a refused target should fail")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("connection was not closed after the remote dial failed")
	}
	// The forward itself stays up for later connections.
	select {
	case <-f.Done():
		t.Fatal("forward stopped after one failed connection")
	default:
	}
}

func TestReverseForward(t *testing.T) {
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "from the client's localhost")
	}))
	defer srv.Close()
	echo := echoServer(t)

	_, hp, _ := net.SplitHostPort(srv.Listener.Addr().String())
	f, err := forward.ListenReverse(ctx, client, mustSpec(t, "0:"+hp))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !f.Reverse() || f.Addr().(*net.TCPAddr).Port == 0 {
		t.Fatalf("addr %v", f.Addr())
	}
	resp, err := http.Get("http://" + f.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "from the client's localhost" {
		t.Fatalf("got %q", b)
	}

	// Raw TCP with half-close and a big payload, three-part spec.
	_, ep, _ := net.SplitHostPort(echo)
	f2, err := forward.ListenReverse(ctx, client, mustSpec(t, "0:127.0.0.1:"+ep))
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	payload := make([]byte, 4<<20)
	rand.Read(payload)
	c, err := net.Dial("tcp", f2.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	go func() { c.Write(payload); c.(*net.TCPConn).CloseWrite() }()
	got, err := io.ReadAll(c)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("reverse echo mismatch: %d bytes, err %v", len(got), err)
	}
	if st := f2.Stats(); st.Connections != 1 {
		t.Fatalf("stats %+v", st)
	}
}

func TestReverseRefusedTargetAndBindError(t *testing.T) {
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)
	_, dp, _ := net.SplitHostPort(closedPort(t))
	f, err := forward.ListenReverse(ctx, client, mustSpec(t, "0:"+dp))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c, err := net.Dial("tcp", f.Addr().String())
	switch {
	case errors.Is(err, syscall.ECONNRESET):
		// Dropped so quickly that the reset beat connect's return: also a drop.
	case err != nil:
		t.Fatal(err)
	default:
		defer c.Close()
		c.SetReadDeadline(time.Now().Add(10 * time.Second))
		if _, err := c.Read(make([]byte, 1)); err == nil {
			t.Fatal("expected the host-side connection to be dropped")
		} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
			t.Fatal("host-side connection not closed")
		}
	}

	// Binding a port that is taken fails with the host's error.
	taken := f.Addr().(*net.TCPAddr).Port
	if _, err := forward.ListenReverse(ctx, client, mustSpec(t, fmt.Sprintf("%d:%s", taken, dp))); err == nil {
		t.Fatal("expected bind error")
	}
}

// A malicious host must not be able to make the client dial arbitrary
// targets: only ids the client registered are served.
func TestClientRejectsUnknownAcceptID(t *testing.T) {
	client, host := peertest.Pair(t)
	ctx := ctxT(t)
	f, err := forward.ListenReverse(ctx, client, mustSpec(t, "0:1"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	s, err := host.Open(ctx, "forward.accept", map[string]string{"id": "bogus"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	b, _ := io.ReadAll(io.LimitReader(s, 4096))
	if !strings.Contains(string(b), "no such reverse forward") {
		t.Fatalf("reply %q", b)
	}
}

func portFree(addr string) bool {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

func TestCloseAndCancelCleanup(t *testing.T) {
	client, _ := peertest.Pair(t)
	addr := echoServer(t)

	// Close: listener released, open connections aborted.
	f, err := forward.Listen(context.Background(), client, specFor(t, 0, addr))
	if err != nil {
		t.Fatal(err)
	}
	la := f.Addr().String()
	c, err := net.Dial("tcp", la)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("ping"))
	buf := make([]byte, 4)
	if _, err := io.ReadFull(c, buf); err != nil {
		t.Fatal(err)
	}
	f.Close()
	f.Close() // idempotent
	if f.Err() != nil {
		t.Fatalf("Err after Close = %v", f.Err())
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(buf); err == nil {
		t.Fatal("open connection survived Close")
	}
	if !portFree(la) {
		t.Fatal("listener still bound after Close")
	}

	// ctx cancel behaves the same, for local and reverse.
	ctx, cancel := context.WithCancel(context.Background())
	fl, err := forward.Listen(ctx, client, specFor(t, 0, addr))
	if err != nil {
		t.Fatal(err)
	}
	fr, err := forward.ListenReverse(ctx, client, specFor(t, 0, addr))
	if err != nil {
		t.Fatal(err)
	}
	ra := fr.Addr().String()
	cancel()
	for _, x := range []*forward.Forwarder{fl, fr} {
		select {
		case <-x.Done():
		case <-time.After(5 * time.Second):
			t.Fatal("forwarder not done after cancel")
		}
		if !errors.Is(x.Err(), context.Canceled) {
			t.Fatalf("Err = %v", x.Err())
		}
	}
	if !portFree(fl.Addr().String()) {
		t.Fatal("local listener leaked after cancel")
	}
	waitFor(t, "host listener closed", func() bool { return portFree(ra) })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestSessionDeathStopsForwarder(t *testing.T) {
	client, host := peertest.Pair(t)
	ctx := ctxT(t)
	addr := echoServer(t)
	f, err := forward.Listen(ctx, client, specFor(t, 0, addr))
	if err != nil {
		t.Fatal(err)
	}
	fr, err := forward.ListenReverse(ctx, client, specFor(t, 0, addr))
	if err != nil {
		t.Fatal(err)
	}
	host.Close()
	for _, x := range []*forward.Forwarder{f, fr} {
		select {
		case <-x.Done():
		case <-time.After(10 * time.Second):
			t.Fatal("forwarder survived session death")
		}
		if !errors.Is(x.Err(), forward.ErrSessionClosed) {
			t.Fatalf("Err = %v", x.Err())
		}
	}
}

func TestNoGoroutineLeak(t *testing.T) {
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)
	addr := echoServer(t)
	// Warm up so lazily started runtime goroutines are in the baseline.
	if c, err := forward.Dial(ctx, client, "tcp", addr); err == nil {
		c.Close()
	}
	time.Sleep(200 * time.Millisecond)
	base := runtime.NumGoroutine()

	for range 3 {
		f, err := forward.Listen(ctx, client, specFor(t, 0, addr))
		if err != nil {
			t.Fatal(err)
		}
		fr, err := forward.ListenReverse(ctx, client, specFor(t, 0, addr))
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range []string{f.Addr().String(), fr.Addr().String()} {
			for range 20 {
				c, err := net.Dial("tcp", a)
				if err != nil {
					t.Fatal(err)
				}
				c.Write([]byte("hi"))
				io.ReadFull(c, make([]byte, 2))
				c.Close()
			}
		}
		// Leave some connections open when closing.
		open, err := net.Dial("tcp", f.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		open.Write([]byte("x"))
		io.ReadFull(open, make([]byte, 1))
		f.Close()
		fr.Close()
		open.Close()
	}
	waitFor(t, "goroutines to settle", func() bool { return runtime.NumGoroutine() <= base+3 })
}

// More simultaneous TCP connections than the session's stream limit: the
// surplus waits for a free stream and completes once earlier connections
// close; nothing is refused, deadlocks or leaks.
func TestMoreConnectionsThanStreams(t *testing.T) {
	client, _ := peertest.Pair(t)
	ctx := ctxT(t)
	addr := echoServer(t)
	f, err := forward.Listen(ctx, client, specFor(t, 0, addr))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	const n = peer.MaxStreams + 76
	release := make(chan struct{})
	var echoed, finished sync.WaitGroup
	errs := make(chan error, n)
	echoed.Add(peer.MaxStreams)
	var once sync.Once
	var served atomic.Int64
	// Connect in bounded waves: a slot is held until the connection has been
	// accepted and echoed. A burst of a thousand simultaneous connects
	// overflows the listen backlog, which Windows answers with a refusal.
	slots := make(chan struct{}, 48)
	for i := range n {
		finished.Add(1)
		go func() {
			defer finished.Done()
			slots <- struct{}{}
			held := true
			free := func() {
				if held {
					held = false
					<-slots
				}
			}
			defer free()
			c, err := net.Dial("tcp", f.Addr().String())
			if err != nil {
				errs <- err
				return
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(50 * time.Second))
			msg := []byte(fmt.Sprintf("%06d", i))
			c.Write(msg)
			got := make([]byte, len(msg))
			if _, err := io.ReadFull(c, got); err != nil || !bytes.Equal(got, msg) {
				errs <- fmt.Errorf("conn %d: %q err %v", i, got, err)
				return
			}
			free()
			// Hold the first wave open so later ones must queue for a stream.
			if served.Add(1) <= peer.MaxStreams {
				echoed.Done()
				<-release
			}
		}()
	}
	waitDone := make(chan struct{})
	go func() { echoed.Wait(); close(waitDone) }()
	select {
	case <-waitDone:
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(40 * time.Second):
		t.Fatal("first wave did not complete")
	}
	once.Do(func() { close(release) })
	finished.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if st := f.Stats(); st.Connections != n {
		t.Fatalf("stats %+v", st)
	}
	waitFor(t, "connections to drain", func() bool { return f.Stats().Active == 0 })
}
