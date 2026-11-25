package dial

import (
	"context"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/signal"
	"github.com/readori/RemoHop/internal/wsdgram"
)

// wsSignal runs a rendezvous server with a relay on loopback UDP and its
// websocket bridge on an httptest server; it returns the UDP address and
// the bridge URL.
func wsSignal(t *testing.T) (addr, url string) {
	t.Helper()
	srv, err := signal.NewServer(signal.Config{DataDir: t.TempDir(), Relay: &signal.RelayConfig{}})
	if err != nil {
		t.Fatal(err)
	}
	main, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	alt, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, main, alt) }()
	ts := httptest.NewServer(srv.WSHandler(signal.WSConfig{}))
	t.Cleanup(func() {
		ts.Close()
		cancel()
		<-done
		srv.Close()
	})
	return main.LocalAddr().String(), "ws" + strings.TrimPrefix(ts.URL, "http") + wsdgram.DefaultPath
}

// blockedUDP is a UDP socket in a network that drops every datagram, as
// where only HTTPS gets out.
type blockedUDP struct {
	once   sync.Once
	closed chan struct{}
	wake   chan struct{}
}

func newBlockedUDP() *blockedUDP {
	return &blockedUDP{closed: make(chan struct{}), wake: make(chan struct{}, 1)}
}

func (b *blockedUDP) ReadFrom([]byte) (int, net.Addr, error) {
	select {
	case <-b.closed:
		return 0, nil, net.ErrClosed
	case <-b.wake:
		return 0, nil, os.ErrDeadlineExceeded
	}
}
func (b *blockedUDP) WriteTo(p []byte, _ net.Addr) (int, error) { return len(p), nil }
func (b *blockedUDP) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}
func (b *blockedUDP) LocalAddr() net.Addr              { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9} }
func (b *blockedUDP) SetDeadline(t time.Time) error    { return b.SetReadDeadline(t) }
func (b *blockedUDP) SetWriteDeadline(time.Time) error { return nil }
func (b *blockedUDP) SetReadDeadline(t time.Time) error {
	if !t.IsZero() && !t.After(time.Now()) {
		select {
		case b.wake <- struct{}{}:
		default:
		}
	}
	return nil
}

// TestEndToEndWebSocket connects through the websocket bridge in every
// combination: a client whose UDP is blocked to a host on UDP, a client on
// UDP to a host whose UDP is blocked, and both blocked.
func TestEndToEndWebSocket(t *testing.T) {
	old := signal.FallbackAfter
	signal.FallbackAfter = 300 * time.Millisecond
	t.Cleanup(func() { signal.FallbackAfter = old })

	for _, tc := range []struct {
		name                 string
		clientWS, hostWS     bool
		wantClientTransport  string
		wantHostPathContains string
	}{
		{"client-ws/host-udp", true, false, TransportWebSocket, "relayed"},
		{"client-udp/host-ws", false, true, TransportUDP, "relayed (websocket)"},
		{"both-ws", true, true, TransportWebSocket, "relayed (websocket)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := testCtx(t)
			sig, url := wsSignal(t)
			hcfg := hostd.Config{Signal: sig, SignalOptions: signal.Options{WS: url}, Password: "ws-test-password"}
			if tc.hostWS {
				hcfg.PacketConn = newBlockedUDP()
			}
			h := startHost(t, hcfg)
			st := h.Status()
			if want := map[bool]string{true: "websocket", false: "udp"}[tc.hostWS]; st.Transport != want {
				t.Fatalf("host transport = %q, want %q", st.Transport, want)
			}

			var notes []string
			var mu sync.Mutex
			opts := Options{Signal: sig, SignalWS: url, Password: "ws-test-password", Dir: t.TempDir(), Locals: noLocals,
				Notify: func(m string) { mu.Lock(); notes = append(notes, m); mu.Unlock() }}
			if tc.clientWS {
				opts.Listen = func() (net.PacketConn, error) { return newBlockedUDP(), nil }
			} else {
				opts.Listen = func() (net.PacketConn, error) { return net.ListenPacket("udp", "127.0.0.1:0") }
			}
			conn, err := Dial(ctx, h.ID(), opts)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			info := InfoOf(conn)
			if info.Path != PathRelayed || info.Transport != tc.wantClientTransport {
				t.Fatalf("path %q transport %q", info.Path, info.Transport)
			}
			echo(t, conn)
			mu.Lock()
			if len(notes) == 0 || !strings.Contains(notes[len(notes)-1], "websocket") {
				t.Errorf("notices = %q", notes)
			}
			mu.Unlock()
			waitFor(t, func() bool { return len(h.Status().Clients) == 1 })
			if p := h.Status().Clients[0].Path; p != tc.wantHostPathContains {
				t.Fatalf("host sees path %q, want %q", p, tc.wantHostPathContains)
			}

			// The next dial from this network goes to the bridge at once
			// instead of waiting for UDP again (signal.UDPBlocked).
			if tc.clientWS {
				udpOpened := false
				again := opts
				again.Listen = func() (net.PacketConn, error) { udpOpened = true; return newBlockedUDP(), nil }
				start := time.Now()
				c2, err := Dial(ctx, h.ID(), again)
				if err != nil {
					t.Fatal(err)
				}
				c2.Close()
				if udpOpened || time.Since(start) > signal.FallbackAfter {
					t.Fatalf("second dial: tried UDP again %v, took %s", udpOpened, time.Since(start))
				}
			}

			// ping says so.
			if tc.clientWS {
				env, stdout, stderr := cliEnv(h.ID(), "ws-test-password", opts)
				if code := runPing(ctx, env, []string{"-c", "1"}); code != 0 {
					t.Fatalf("ping exit %d: %s", code, stderr)
				}
				if !strings.Contains(stdout.String(), "relayed path (websocket)") {
					t.Fatalf("ping output: %s", stdout)
				}
			}
		})
	}
}

// TestWebSocketForcedAndFailing: REMOHOP_TRANSPORT=ws skips UDP entirely;
// forced UDP never touches the bridge; a dead bridge is reported.
func TestWebSocketForcedAndFailing(t *testing.T) {
	ctx := testCtx(t)
	sig, url := wsSignal(t)
	h := startHost(t, hostd.Config{Signal: sig, Password: "ws-test-password"})

	udpOpened := false
	opts := Options{Signal: sig, SignalWS: url, Transport: signal.TransportWS, Password: "ws-test-password", Dir: t.TempDir(), Locals: noLocals,
		Listen: func() (net.PacketConn, error) { udpOpened = true; return net.ListenPacket("udp", "127.0.0.1:0") }}
	conn, err := Dial(ctx, h.ID(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if udpOpened || InfoOf(conn).Transport != TransportWebSocket {
		t.Fatalf("forced ws: udp opened %v, transport %q", udpOpened, InfoOf(conn).Transport)
	}
	conn.Close()

	opts.Transport = signal.TransportUDP
	opts.Listen = func() (net.PacketConn, error) { return newBlockedUDP(), nil }
	sctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := Dial(sctx, h.ID(), opts); err == nil {
		t.Fatal("forced UDP through a black hole connected")
	}

	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := "ws://" + ln.Addr().String() + "/remohop/v1/udp"
	ln.Close()
	opts.Transport, opts.SignalWS = signal.TransportWS, dead
	if _, err := Dial(ctx, h.ID(), opts); err == nil || !strings.Contains(err.Error(), "websocket bridge") {
		t.Fatalf("dead bridge: %v", err)
	}
}
