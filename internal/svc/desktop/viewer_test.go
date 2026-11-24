package desktop

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func startTestViewer(t *testing.T) *viewer {
	t.Helper()
	v, err := newViewer(nil, Options{}, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go v.serve()
	t.Cleanup(v.close)
	return v
}

func get(t *testing.T, rawURL, host string) *http.Response {
	t.Helper()
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestViewerHTTPAccess(t *testing.T) {
	v := startTestViewer(t)
	base := fmt.Sprintf("http://127.0.0.1:%d", v.port)
	if !strings.HasPrefix(v.URL(), base+"/") || len(v.token) < 20 {
		t.Fatalf("url %s", v.URL())
	}
	cases := []struct {
		name, url, host string
		want            int
	}{
		{"page", v.URL(), "", 200},
		{"localhost", v.URL(), fmt.Sprintf("localhost:%d", v.port), 200},
		{"no token", base + "/", "", 404},
		{"wrong token", base + "/AAAAAAAAAAAAAAAAAAAAAA/", "", 404},
		{"token prefix", base + "/" + v.token[:10] + "/", "", 404},
		{"unknown path", v.URL() + "x.js", "", 404},
		{"rebinding host", v.URL(), fmt.Sprintf("evil.example:%d", v.port), 403},
		{"other port", v.URL(), "127.0.0.1:1", 403},
	}
	for _, c := range cases {
		resp := get(t, c.url, c.host)
		if resp.StatusCode != c.want {
			t.Errorf("%s: status %d, want %d", c.name, resp.StatusCode, c.want)
		}
		if c.want == 200 {
			csp := resp.Header.Get("Content-Security-Policy")
			if !strings.Contains(csp, "default-src 'none'") || !strings.Contains(csp, "frame-ancestors 'none'") {
				t.Errorf("%s: CSP %q", c.name, csp)
			}
			if resp.Header.Get("Referrer-Policy") != "no-referrer" {
				t.Errorf("%s: no Referrer-Policy", c.name)
			}
		}
	}
}

// rawUpgrade sends a WebSocket upgrade request with the given Origin (empty:
// none) and returns the HTTP status.
func rawUpgrade(t *testing.T, v *viewer, path, origin string) int {
	t.Helper()
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", v.port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: 127.0.0.1:%d\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n", path, v.port)
	if origin != "" {
		req += "Origin: " + origin + "\r\n"
	}
	c.Write([]byte(req + "\r\n"))
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode
}

func TestViewerWebSocketOrigin(t *testing.T) {
	v := startTestViewer(t)
	ws := "/" + v.token + "/ws"
	for _, o := range []string{"", "http://evil.example", "null", fmt.Sprintf("http://127.0.0.1:%d.evil.example", v.port),
		"http://127.0.0.1:1", fmt.Sprintf("https://127.0.0.1:%d", v.port)} {
		if code := rawUpgrade(t, v, ws, o); code != http.StatusForbidden {
			t.Errorf("origin %q: status %d, want 403", o, code)
		}
	}
	if code := rawUpgrade(t, v, "/wrong/ws", fmt.Sprintf("http://127.0.0.1:%d", v.port)); code != 404 {
		t.Errorf("wrong token upgrade: status %d", code)
	}
}

// dialViewer connects to the viewer's WebSocket as the page would.
func dialViewer(t *testing.T, v *viewer) *websocket.Conn {
	t.Helper()
	return dialURL(t, v.URL())
}

// dialURL connects to the WebSocket of the viewer at page URL raw.
func dialURL(t *testing.T, raw string) *websocket.Conn {
	t.Helper()
	u, _ := url.Parse(raw)
	cfg, err := websocket.NewConfig("ws://"+u.Host+u.Path+"ws", "http://"+u.Host)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	return ws
}

func TestParseRate(t *testing.T) {
	good := map[string]int64{"8M": 8_000_000, "2500k": 2_500_000, "1.5m": 1_500_000, "800000": 800_000, "10Mbps": 10_000_000}
	for s, want := range good {
		if got, err := parseRate(s); err != nil || got != want {
			t.Errorf("%s: %d %v", s, got, err)
		}
	}
	for _, s := range []string{"", "x", "-5M", "1k", "9G", "NaN"} {
		if _, err := parseRate(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
}
