package web

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
)

func TestPublicURLValidation(t *testing.T) {
	testEnv(t)
	pw := writePasswordFile(t, "correct horse")
	for name, cfg := range map[string]Config{
		"no password":  {PublicURL: "https://desk.example.com"},
		"not loopback": {Listen: "0.0.0.0:0", PasswordFile: pw, PublicURL: "https://desk.example.com"},
		"http":         {PasswordFile: pw, PublicURL: "http://desk.example.com"},
		"path":         {PasswordFile: pw, PublicURL: "https://desk.example.com/remohop"},
		"wildcard":     {PasswordFile: pw, PublicURL: "https://*.example.com"},
		"bad port":     {PasswordFile: pw, PublicURL: "https://desk.example.com:0"},
	} {
		if s, err := New(cfg); err == nil {
			s.Close()
			t.Errorf("%s: accepted", name)
		}
	}
}

// A tunnel (cloudflared) forwards https://desk.example.com to the plain-HTTP
// loopback gateway with the browser's Host and Origin unchanged.
func TestPublicURLThroughTunnel(t *testing.T) {
	testEnv(t)
	s := startServer(t, Config{Listen: "127.0.0.1:0", PasswordFile: writePasswordFile(t, "correct horse"),
		PublicURL: "https://desk.example.com/", RealIPHeader: "CF-Connecting-IP"})
	if s.URL() != "https://desk.example.com/" {
		t.Fatalf("URL = %q", s.URL())
	}
	local := "http://" + s.Addr().String() + "/"
	jar, _ := cookiejar.New(nil)
	hc := &http.Client{Jar: jar}
	call := func(method, path, host, origin, cfip string, body any) (*http.Response, []byte) {
		t.Helper()
		var rd io.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
		req, _ := http.NewRequest(method, local+path, rd)
		req.Host = host
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if cfip != "" {
			req.Header.Set("CF-Connecting-IP", cfip)
		}
		r, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		b, _ := io.ReadAll(r.Body)
		return r, b
	}
	const pub, porigin = "desk.example.com", "https://desk.example.com"

	r, _ := call("GET", "", pub, "", "", nil)
	if r.StatusCode != 200 || r.Header.Get("Strict-Transport-Security") == "" ||
		!strings.Contains(r.Header.Get("Content-Security-Policy"), "wss://desk.example.com") {
		t.Fatalf("index via public host: %d hsts=%q csp=%q", r.StatusCode,
			r.Header.Get("Strict-Transport-Security"), r.Header.Get("Content-Security-Policy"))
	}
	if r, _ := call("GET", "", "other.example.com", "", "", nil); r.StatusCode != 403 {
		t.Errorf("foreign Host: %d", r.StatusCode)
	}
	if r, _ := call("GET", "", "desk.example.com:8443", "", "", nil); r.StatusCode != 403 {
		t.Errorf("public host on another port: %d", r.StatusCode)
	}
	// the browser's Origin is https even though the gateway speaks http
	if r, _ := call("POST", "api/v1/login", pub, "http://desk.example.com", "", map[string]any{"password": "x"}); r.StatusCode != 403 {
		t.Errorf("http origin accepted: %d", r.StatusCode)
	}
	// failures from one visitor do not lock out another
	for i := 0; i < freeFailures+1; i++ {
		call("POST", "api/v1/login", pub, porigin, "203.0.113.7", map[string]any{"password": "wrong"})
	}
	if r, _ := call("POST", "api/v1/login", pub, porigin, "203.0.113.7", map[string]any{"password": "wrong"}); r.StatusCode != 429 {
		t.Errorf("guessing visitor not locked out: %d", r.StatusCode)
	}
	r, body := call("POST", "api/v1/login", pub, porigin, "198.51.100.9", map[string]any{"password": "correct horse"})
	if r.StatusCode != 200 {
		t.Fatalf("login from another visitor: %d %s", r.StatusCode, body)
	}
	var ck *http.Cookie
	for _, c := range r.Cookies() {
		ck = c
	}
	if ck == nil || ck.Name != "__Host-remohop" || !ck.Secure || !ck.HttpOnly {
		t.Fatalf("session cookie %+v, want a Secure __Host- cookie", ck)
	}
}
