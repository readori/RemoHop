package account_test

import (
	"context"
	"net"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/account"
	"github.com/readori/RemoHop/internal/signal"
	"github.com/readori/RemoHop/internal/wsdgram"
)

// blockedUDP is a UDP socket in a network that drops every datagram.
type blockedUDP struct {
	once   sync.Once
	closed chan struct{}
	wake   chan struct{}
}

func newBlockedUDP() (net.PacketConn, error) {
	return &blockedUDP{closed: make(chan struct{}), wake: make(chan struct{}, 1)}, nil
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

// TestAccountsOverTheBridge: with UDP unavailable, logging in, joining,
// approving and refreshing all work through the server's websocket bridge,
// both with a bridge-only configuration and with UDP configured but
// blocked (the fallback).
func TestAccountsOverTheBridge(t *testing.T) {
	old := signal.FallbackAfter
	signal.FallbackAfter = 300 * time.Millisecond
	t.Cleanup(func() { signal.FallbackAfter = old })
	t.Setenv("REMOHOP_SIGNAL_WS", "")
	t.Setenv("REMOHOP_TRANSPORT", "")

	s := startServer(t)
	ts := httptest.NewServer(s.srv.WSHandler(signal.WSConfig{}))
	defer ts.Close()
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + wsdgram.DefaultPath
	email := "bridge@example.com"
	ctx := testCtx(t)
	// Wait until the server is serving (the bridge answers 503 before).
	for start := time.Now(); ; time.Sleep(20 * time.Millisecond) {
		wc, _ := wsdgram.New(wsdgram.Options{URL: url})
		err := wc.Connect(ctx)
		wc.Close()
		if err == nil {
			break
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal(err)
		}
	}

	// Device a knows only the bridge.
	a := newDir(t, "ci-runner")
	setConfig(t, a, "signal-ws", url)
	t.Setenv("REMOHOP_CONFIG_DIR", a)
	optsA := account.Options{Dir: a, Listen: newBlockedUDP}
	if err := account.RequestCode(ctx, optsA, email); err != nil {
		t.Fatalf("request code over the bridge: %v", err)
	}
	res, err := account.Login(ctx, optsA, account.LoginRequest{Email: email, Code: s.mail.code(t, email)})
	if err != nil || res.Status != "created" {
		t.Fatalf("login over the bridge: %+v %v", res, err)
	}
	st, err := account.Load(a)
	if err != nil || st.Server != "" || st.ServerWS != url {
		t.Fatalf("state after a bridge login: %+v %v", st, err)
	}

	// Device b has UDP configured, but it is blocked: it falls back.
	b := newDir(t, "laptop")
	setConfig(t, b, "signal", s.addr)
	setConfig(t, b, "signal-ws", url)
	t.Setenv("REMOHOP_CONFIG_DIR", b)
	optsB := account.Options{Dir: b, Listen: newBlockedUDP}
	if err := account.RequestCode(ctx, optsB, email); err != nil {
		t.Fatal(err)
	}
	jb, err := account.Login(ctx, optsB, account.LoginRequest{Email: email, Code: s.mail.code(t, email), AccountFingerprint: st.Fingerprint()})
	if err != nil || jb.Status != "pending" {
		t.Fatalf("join with the fallback: %+v %v", jb, err)
	}
	if stb, _ := account.Load(b); stb.Server != s.addr || stb.ServerWS != url {
		t.Fatalf("state after the fallback login: %+v", stb)
	}

	// Later operations reach the server from the recorded state.
	t.Setenv("REMOHOP_CONFIG_DIR", a)
	if _, err := account.Approve(ctx, optsA, jb.Device); err != nil {
		t.Fatalf("approve over the bridge: %v", err)
	}
	t.Setenv("REMOHOP_CONFIG_DIR", b)
	if stb, err := account.Refresh(ctx, optsB); err != nil || stb.Status != account.StatusMember {
		t.Fatalf("refresh with the fallback: %+v %v", stb, err)
	}
	t.Setenv("REMOHOP_CONFIG_DIR", a)
	devs, _, err := account.Devices(context.Background(), optsA)
	if err != nil || len(devs) != 2 {
		t.Fatalf("devices over the bridge: %+v %v", devs, err)
	}
}
