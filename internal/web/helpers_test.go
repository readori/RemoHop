package web

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

// testEnv isolates the config dir and the headless clipboard/shell.
func testEnv(t *testing.T) {
	t.Helper()
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("SHELL", "/bin/sh")
}

// pairDial returns a Dial seam that hands out peertest sessions and checks
// the password "hostpw".
type dialer struct {
	t     *testing.T
	mu    sync.Mutex
	calls []DialRequest
	opts  peertest.Options
	hosts []*peer.Conn
	// over is set when the test's cleanups start: closing the test pairs
	// looks like a drop to a persistent gateway, which would otherwise
	// redial through t after the test has ended.
	over atomic.Bool
}

func (d *dialer) dial(ctx context.Context, req DialRequest) (*peer.Conn, error) {
	d.mu.Lock()
	d.calls = append(d.calls, DialRequest{Target: req.Target, Password: req.Password})
	d.mu.Unlock()
	if req.Notify != nil {
		req.Notify("waiting for approval on the host…")
	}
	if err := ctx.Err(); err != nil {
		return nil, err // the gateway is closing
	}
	if d.over.Load() {
		return nil, errors.New("the test is over")
	}
	if req.Password != "hostpw" {
		return nil, &peer.RemoteError{Service: "login", Message: "wrong password"}
	}
	c, h := peertest.PairOpts(d.t, peer.HostServices, d.opts)
	// Registered after the pair's own cleanups, so it runs before them.
	d.t.Cleanup(func() { d.over.Store(true) })
	d.mu.Lock()
	d.hosts = append(d.hosts, h)
	d.mu.Unlock()
	return c, nil
}

func startServer(t *testing.T, cfg Config) *Server {
	t.Helper()
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := s.Start()
	t.Cleanup(func() {
		s.Close()
		<-done
	})
	return s
}

// client is a tiny API client for a loopback gateway.
type client struct {
	t      *testing.T
	s      *Server
	base   string
	http   *http.Client
	sid    string
	csrf   string
	origin string
}

func newLoopbackClient(t *testing.T, s *Server) *client {
	t.Helper()
	u := s.URL()
	c := &client{t: t, s: s, base: u, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	c.origin = strings.TrimSuffix(u[:strings.Index(u[8:], "/")+8], "/")
	if strings.HasPrefix(u, "http://") {
		c.origin = u[:strings.Index(u[7:], "/")+7]
	}
	return c
}

func (c *client) newSession() {
	c.t.Helper()
	code, body := c.do("POST", "api/v1/session", map[string]any{}, nil)
	if code != 200 {
		c.t.Fatalf("session: %d %s", code, body)
	}
	var j struct{ SID, CSRF string }
	var raw map[string]any
	json.Unmarshal(body, &raw)
	j.SID, _ = raw["sid"].(string)
	j.CSRF, _ = raw["csrf"].(string)
	c.sid, c.csrf = j.SID, j.CSRF
}

// do performs a JSON call with the session headers, Origin and CSRF.
func (c *client) do(method, path string, body any, hdr map[string]string) (int, []byte) {
	c.t.Helper()
	var rd io.Reader
	ct := ""
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
		ct = "application/octet-stream"
	case io.Reader:
		rd = b
		ct = "application/octet-stream"
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
		ct = "application/json"
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	if c.sid != "" {
		req.Header.Set(hdrSession, c.sid)
	}
	if c.csrf != "" {
		req.Header.Set(hdrCSRF, c.csrf)
	}
	if method != "GET" {
		req.Header.Set("Origin", c.origin)
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func (c *client) jsonCall(method, path string, body any, out any) int {
	c.t.Helper()
	code, b := c.do(method, path, body, nil)
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("%s %s: bad JSON %q", method, path, b)
		}
	}
	return code
}

// connect runs the connect flow and returns the connection key.
func (c *client) connect(target, password string) string {
	c.t.Helper()
	var a map[string]any
	if code := c.jsonCall("POST", "api/v1/connect", map[string]any{"target": target, "password": password}, &a); code != 202 {
		c.t.Fatalf("connect: %d %v", code, a)
	}
	id, _ := a["attempt"].(string)
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		var j map[string]any
		c.jsonCall("GET", "api/v1/connect/"+id, nil, &j)
		switch j["state"] {
		case "connected":
			return j["conn"].(map[string]any)["key"].(string)
		case "failed", "canceled":
			c.t.Fatalf("connect failed: %v", j)
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.t.Fatal("connect timed out")
	return ""
}

// ---- TLS ----

func writeTestCert(t *testing.T) (certFile, keyFile string) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "remohop-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{"localhost", "gateway.test"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalPKCS8PrivateKey(key)
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}), 0o600)
	return
}

func writePasswordFile(t *testing.T, pw string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gwpass")
	if err := os.WriteFile(p, []byte(pw+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func tlsClient() (*http.Client, *cookiejar.Jar) {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Timeout: 30 * time.Second,
		Transport:     &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, jar
}

// fakeClock is a settable clock safe for the gateway's goroutines.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *fakeClock          { return &fakeClock{t: time.Now()} }
func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}
