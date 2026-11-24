package wsdgram

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

// echoServer is a minimal far end: it says hello and sends every datagram
// back from the port it was addressed to.
type echoServer struct {
	headers chan http.Header
	conns   atomic.Int32
	drop    chan struct{} // closing it drops every open connection
	mu      sync.Mutex
	open    []*websocket.Conn
}

func newEchoServer() *echoServer {
	return &echoServer{headers: make(chan http.Header, 16), drop: make(chan struct{})}
}

func (e *echoServer) handler() http.Handler {
	return websocket.Server{
		Handshake: func(c *websocket.Config, r *http.Request) error {
			if r.Header.Get("Origin") != OriginValue {
				return errors.New("bad origin")
			}
			if len(c.Protocol) != 1 || c.Protocol[0] != Subprotocol {
				return errors.New("bad protocol")
			}
			select {
			case e.headers <- r.Header.Clone():
			default:
			}
			return nil
		},
		Handler: func(ws *websocket.Conn) {
			e.conns.Add(1)
			e.mu.Lock()
			e.open = append(e.open, ws)
			e.mu.Unlock()
			websocket.Message.Send(ws, Hello(4000, 4001))
			for {
				var m []byte
				if err := websocket.Message.Receive(ws, &m); err != nil {
					return
				}
				port, payload, err := ParseData(m)
				if err != nil {
					continue
				}
				websocket.Message.Send(ws, AppendData(nil, port, payload))
			}
		},
	}
}

func (e *echoServer) dropAll() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, ws := range e.open {
		ws.Close()
	}
	e.open = nil
}

func wsURL(httpURL string) string { return "ws" + strings.TrimPrefix(httpURL, "http") + DefaultPath }

func noProxy(*url.URL) (*url.URL, error) { return nil, nil }

func roundTrip(t *testing.T, c *Conn, port uint16, msg string) {
	t.Helper()
	to := &net.UDPAddr{IP: ServerIP.AsSlice(), Port: int(port)}
	if _, err := c.WriteTo([]byte(msg), to); err != nil {
		t.Fatal(err)
	}
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 100)
	n, from, err := c.ReadFrom(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf[:n]) != msg {
		t.Fatalf("got %q, want %q", buf[:n], msg)
	}
	if ap := from.(*net.UDPAddr).AddrPort(); ap.Addr().Unmap() != ServerIP || ap.Port() != port {
		t.Fatalf("from %v", from)
	}
}

func TestFraming(t *testing.T) {
	m := AppendData(nil, 21116, []byte("abc"))
	port, p, err := ParseData(m)
	if err != nil || port != 21116 || string(p) != "abc" {
		t.Fatalf("ParseData = %d %q %v", port, p, err)
	}
	for _, bad := range [][]byte{nil, {msgData, 0}, {msgHello, 0, 0}, append([]byte{msgData, 0, 1}, make([]byte, MaxDatagram+1)...)} {
		if _, _, err := ParseData(bad); err == nil {
			t.Errorf("ParseData(%x...) accepted", bad[:min(len(bad), 4)])
		}
	}
	if a, b, err := parseHello(Hello(1, 2)); err != nil || a != 1 || b != 2 {
		t.Fatalf("hello = %d %d %v", a, b, err)
	}
	if _, _, err := parseHello(Hello(0, 2)); err == nil {
		t.Fatal("hello without port accepted")
	}
}

func TestParseURL(t *testing.T) {
	for in, want := range map[string]string{
		"wss://relay.example.com":         "wss://relay.example.com" + DefaultPath,
		"wss://relay.example.com/":        "wss://relay.example.com" + DefaultPath,
		"ws://127.0.0.1:8080/x/y":         "ws://127.0.0.1:8080/x/y",
		" wss://relay.example.com/a#frag": "wss://relay.example.com/a",
	} {
		u, err := ParseURL(in)
		if err != nil || u.String() != want {
			t.Errorf("ParseURL(%q) = %v, %v; want %s", in, u, err, want)
		}
	}
	for _, bad := range []string{"", "https://relay.example.com", "relay.example.com", "wss://", "wss://user:pw@relay.example.com/"} {
		if _, err := ParseURL(bad); err == nil {
			t.Errorf("ParseURL(%q) accepted", bad)
		}
	}
}

func TestConnRoundTrip(t *testing.T) {
	e := newEchoServer()
	ts := httptest.NewServer(e.handler())
	defer ts.Close()
	c, err := New(Options{URL: wsURL(ts.URL), Proxy: noProxy, Header: http.Header{"Cf-Access-Client-Id": {"id.access"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Written before anything is connected: queued, then delivered.
	roundTrip(t, c, 4000, "first")
	if got := c.ServerAddr(); got != netip.AddrPortFrom(ServerIP, 4000) {
		t.Fatalf("ServerAddr = %v", got)
	}
	h := <-e.headers
	if h.Get("Cf-Access-Client-Id") != "id.access" {
		t.Fatalf("access header not sent: %v", h)
	}
	roundTrip(t, c, 4001, "second")
	if !IsBridge(c.LocalAddr()) || c.LocalAddr().Network() != "websocket" {
		t.Fatalf("LocalAddr = %#v", c.LocalAddr())
	}

	// Only the server is reachable.
	if _, err := c.WriteTo([]byte("x"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4000}); err == nil {
		t.Fatal("write to another address accepted")
	}
	if _, err := c.WriteTo(make([]byte, MaxDatagram+1), &net.UDPAddr{IP: ServerIP.AsSlice(), Port: 4000}); err == nil {
		t.Fatal("oversized datagram accepted")
	}

	// Deadlines.
	c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	if _, _, err := c.ReadFrom(make([]byte, 10)); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read past deadline: %v", err)
	}
	// A deadline set while a read waits wakes it.
	c.SetReadDeadline(time.Time{})
	errc := make(chan error, 1)
	go func() {
		_, _, err := c.ReadFrom(make([]byte, 10))
		errc <- err
	}()
	time.Sleep(50 * time.Millisecond)
	c.SetReadDeadline(time.Now())
	if err := <-errc; !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("read woken by deadline: %v", err)
	}

	// The WebSocket drops: the next writes open a new one.
	e.dropAll()
	waitUntil(t, func() bool { return !c.Connected() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		c.WriteTo([]byte("again"), &net.UDPAddr{IP: ServerIP.AsSlice(), Port: 4000})
		c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		buf := make([]byte, 10)
		if n, _, err := c.ReadFrom(buf); err == nil && string(buf[:n]) == "again" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no reconnect")
		}
	}
	if e.conns.Load() < 2 {
		t.Fatalf("connections = %d", e.conns.Load())
	}

	// Close ends pending reads.
	c.SetReadDeadline(time.Time{})
	go func() {
		_, _, err := c.ReadFrom(make([]byte, 10))
		errc <- err
	}()
	time.Sleep(20 * time.Millisecond)
	c.Close()
	if err := <-errc; !errors.Is(err, net.ErrClosed) {
		t.Fatalf("read after close: %v", err)
	}
	if _, err := c.WriteTo([]byte("x"), &net.UDPAddr{IP: ServerIP.AsSlice(), Port: 4000}); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("write after close: %v", err)
	}
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	for start := time.Now(); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Since(start) > 10*time.Second {
			t.Fatal("timed out")
		}
	}
}

func TestConnectErrors(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Something that is not a WebSocket endpoint: the status is reported.
	forbidden := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	}))
	defer forbidden.Close()
	c, _ := New(Options{URL: wsURL(forbidden.URL), Proxy: noProxy})
	err := c.Connect(ctx)
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 403 || !strings.Contains(err.Error(), "Access") {
		t.Fatalf("Connect to a 403 = %v", err)
	}
	c.Close()

	// A WebSocket endpoint that is not ours: no hello.
	other := httptest.NewServer(websocket.Server{
		Handshake: func(*websocket.Config, *http.Request) error { return nil },
		Handler: func(ws *websocket.Conn) {
			websocket.Message.Send(ws, "hello there")
			time.Sleep(time.Second)
		},
	})
	defer other.Close()
	c, _ = New(Options{URL: wsURL(other.URL), Proxy: noProxy})
	if err := c.Connect(ctx); err == nil || !strings.Contains(err.Error(), "not a remohop endpoint") {
		t.Fatalf("Connect to a foreign endpoint = %v", err)
	}
	c.Close()

	// Nothing listening.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()
	c, _ = New(Options{URL: "ws://" + addr, Proxy: noProxy})
	if err := c.Connect(ctx); err == nil {
		t.Fatal("Connect to nothing succeeded")
	}
	c.Close()
}

func TestTLSAndProxy(t *testing.T) {
	e := newEchoServer()
	ts := httptest.NewTLSServer(e.handler())
	defer ts.Close()

	var connects atomic.Int32
	var sawAuth atomic.Value
	proxy := connectProxy(t, func(r *http.Request) { connects.Add(1); sawAuth.Store(r.Header.Get("Proxy-Authorization")) })
	defer proxy.Close()

	proxyURL, _ := url.Parse("http://alice:s3cret@" + proxy.Addr().String())
	var asked atomic.Value
	c, err := New(Options{
		URL:       "wss" + strings.TrimPrefix(ts.URL, "https") + "/remohop/v1/udp",
		TLSConfig: ts.Client().Transport.(*http.Transport).TLSClientConfig,
		Proxy: func(u *url.URL) (*url.URL, error) {
			asked.Store(u.String())
			return proxyURL, nil
		},
		Header: http.Header{"Cf-Access-Client-Secret": {"top-secret"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	roundTrip(t, c, 4000, "through the proxy")
	if connects.Load() != 1 {
		t.Fatalf("CONNECTs = %d", connects.Load())
	}
	if want := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cret")); sawAuth.Load() != want {
		t.Fatalf("Proxy-Authorization = %v", sawAuth.Load())
	}
	if !strings.HasPrefix(asked.Load().(string), "https://127.0.0.1:") {
		t.Fatalf("proxy asked for %v", asked.Load())
	}
	if h := <-e.headers; h.Get("Cf-Access-Client-Secret") != "top-secret" {
		t.Fatal("access secret not sent")
	}

	// A proxy that wants other credentials: the refusal is explained and
	// the password is not repeated.
	refusing := connectProxy(t, nil)
	defer refusing.Close()
	bad, _ := url.Parse("http://alice:wrong-pass@" + refusing.Addr().String())
	c2, _ := New(Options{URL: "wss://relay.invalid/", Proxy: func(*url.URL) (*url.URL, error) { return bad, nil }})
	defer c2.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = c2.Connect(ctx)
	if err == nil || !strings.Contains(err.Error(), "407") || strings.Contains(err.Error(), "wrong-pass") {
		t.Fatalf("Connect through a refusing proxy = %v", err)
	}
}

// connectProxy is a tiny HTTP CONNECT proxy. With a nil hook it answers
// 407 to everything.
func connectProxy(t *testing.T, hook func(*http.Request)) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				br := bufio.NewReader(c)
				req, err := http.ReadRequest(br)
				if err != nil || req.Method != http.MethodConnect {
					return
				}
				if hook == nil {
					io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n")
					return
				}
				hook(req)
				up, err := net.Dial("tcp", req.Host)
				if err != nil {
					io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
					return
				}
				defer up.Close()
				io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\n")
				go func() {
					if br.Buffered() > 0 {
						b, _ := br.Peek(br.Buffered())
						up.Write(bytes.Clone(b))
					}
					io.Copy(up, c)
				}()
				io.Copy(c, up)
			}()
		}
	}()
	return ln
}
