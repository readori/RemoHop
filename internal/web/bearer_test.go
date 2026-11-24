package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/readori/RemoHop/internal/peer/peertest"
)

// A non-browser client (e.g. behind a proxy that injects the header) uses
// the gateway with "Authorization: Bearer <password>" alone: no login call,
// no cookie, no CSRF token, no Origin.
func TestBearerClient(t *testing.T) {
	testEnv(t)
	d := &dialer{t: t, opts: peertest.Options{}}
	s := startServer(t, Config{Listen: "127.0.0.1:0", PasswordFile: writePasswordFile(t, "gateway secret"), Dial: d.dial})
	base := "http://" + s.Addr().String() + "/"
	hc := &http.Client{Timeout: 30 * time.Second}
	call := func(method, path, bearer string, body any) (int, map[string]any) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, base+path, rd)
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var m map[string]any
		json.NewDecoder(resp.Body).Decode(&m)
		if len(resp.Header.Values("Set-Cookie")) != 0 {
			t.Errorf("%s %s set a cookie for a bearer client", method, path)
		}
		return resp.StatusCode, m
	}

	if code, _ := call("GET", "api/v1/connections", "", nil); code != 401 {
		t.Fatalf("no credentials: %d", code)
	}
	if code, m := call("GET", "api/v1/connections", "wrong password", nil); code != 401 || m["error"].(map[string]any)["code"] != "bad_password" {
		t.Fatalf("wrong bearer: %d %v", code, m)
	}
	if code, _ := call("GET", "api/v1/connections", "gateway secret", nil); code != 200 {
		t.Fatalf("bearer GET: %d", code)
	}

	// A state-changing call without CSRF token or Origin.
	code, a := call("POST", "api/v1/connect", "gateway secret", map[string]any{"target": "123456789", "password": "hostpw"})
	if code != 202 {
		t.Fatalf("connect: %d %v", code, a)
	}
	var key string
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline) && key == ""; time.Sleep(20 * time.Millisecond) {
		_, j := call("GET", "api/v1/connect/"+a["attempt"].(string), "gateway secret", nil)
		switch j["state"] {
		case "connected":
			key = j["conn"].(map[string]any)["key"].(string)
		case "failed", "canceled":
			t.Fatalf("connect failed: %v", j)
		}
	}
	if key == "" {
		t.Fatal("connect timed out")
	}
	// Requests share one session: the connection is visible on the next call.
	if _, m := call("GET", "api/v1/connections", "gateway secret", nil); len(m["conns"].([]any)) != 1 {
		t.Fatalf("connections after connect: %v", m)
	}

	// Terminal WebSocket: ticket plus the header; the Origin is irrelevant.
	_, tk := call("POST", "api/v1/ticket", "gateway secret", map[string]any{"purpose": "ws"})
	q := url.Values{"c": {key}, "rows": {"24"}, "cols": {"80"}, "session": {"api"}, "t": {tk["ticket"].(string)}}
	cfg, err := websocket.NewConfig("ws://"+s.Addr().String()+"/api/v1/term/ws?"+q.Encode(), "https://unrelated.example")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Header = http.Header{"Authorization": {"Bearer gateway secret"}}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		t.Fatalf("terminal websocket: %v", err)
	}
	defer ws.Close()
	readControl(t, ws, "attached")
	sendTerm(t, ws, map[string]any{"type": "in", "data": "echo bearer-$((6*7))\r"})
	readUntil(t, ws, "bearer-42")

	// Without the header the same kind of ticket needs a cookie session.
	_, tk = call("POST", "api/v1/ticket", "gateway secret", map[string]any{"purpose": "ws"})
	q.Set("t", tk["ticket"].(string))
	cfg2, _ := websocket.NewConfig("ws://"+s.Addr().String()+"/api/v1/term/ws?"+q.Encode(), "http://"+s.Addr().String())
	if ws2, err := websocket.DialConfig(cfg2); err == nil {
		ws2.Close()
		t.Fatal("a ticket was redeemed without the bearer header or a cookie")
	}
}

// On a loopback gateway without a password the header means nothing.
func TestBearerIgnoredWithoutPassword(t *testing.T) {
	testEnv(t)
	s := startServer(t, Config{Listen: "127.0.0.1:0"})
	req, _ := http.NewRequest("GET", s.URL()+"api/v1/connections", nil)
	req.Header.Set("Authorization", "Bearer anything")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("got %d", resp.StatusCode)
	}
}
