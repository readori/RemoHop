package mobile

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/hostd"
)

const hostPassword = "mobile-test-password"

// initDir points the package at a fresh directory and stops any gateway a
// previous test left running.
func initDir(t *testing.T) string {
	t.Helper()
	StopGateway()
	d := filepath.Join(t.TempDir(), "files", "remohop")
	if err := Init(d, ""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(StopGateway)
	return d
}

// startHost runs a host daemon reachable at 127.0.0.1:port, with no
// rendezvous server, the way a host on the same LAN is dialled by address.
func startHost(t *testing.T) string {
	t.Helper()
	h, err := hostd.New(hostd.Config{Dir: t.TempDir(), Listen: "127.0.0.1:0", Password: hostPassword,
		Locals: func() []netip.Addr { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("host: %v", err)
		}
	})
	return h.Addr().String()
}

func TestInit(t *testing.T) {
	StopGateway()
	if err := Init("relative/dir", ""); err == nil {
		t.Fatal("Init accepted a relative directory")
	}
	if _, err := DevicesJSON(false); err == nil && dir == "" {
		t.Fatal("DevicesJSON worked before Init")
	}
	t.Setenv("TMPDIR", os.Getenv("TMPDIR")) // restored after the test
	t.Setenv("REMOHOP_CONFIG_DIR", "")
	d := filepath.Join(t.TempDir(), "files", "remohop")
	cache := filepath.Join(t.TempDir(), "cache")
	if err := Init(d, cache); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(StopGateway)
	if os.Getenv("TMPDIR") != cache {
		t.Fatal("TMPDIR not set to the app's cache directory")
	}
	fi, err := os.Stat(d)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 { // Windows has no mode bits
		t.Fatalf("config dir mode %v, want 0700", fi.Mode().Perm())
	}
	if got := os.Getenv("REMOHOP_CONFIG_DIR"); got != d {
		t.Fatalf("REMOHOP_CONFIG_DIR = %q, want %q", got, d)
	}
	// Same directory again (an Activity recreated): fine, even while serving.
	if _, err := StartGateway(); err != nil {
		t.Fatal(err)
	}
	if err := Init(d, ""); err != nil {
		t.Fatalf("re-Init with the same dir: %v", err)
	}
	if err := Init(t.TempDir(), ""); err == nil {
		t.Fatal("Init switched directories under a running gateway")
	}
}

func TestSettings(t *testing.T) {
	d := initDir(t)
	for _, bad := range []string{":21116", "sig example.net", "sig.example.net:99999", "sig.example.net:http", "a/b"} {
		if err := SetSetting("signal", bad); err == nil {
			t.Fatalf("bad rendezvous address %q accepted", bad)
		}
	}
	if err := SetSetting("host-password", "x"); err == nil {
		t.Fatal("unmanaged setting accepted")
	}
	if _, err := GetSetting("host-password"); err == nil {
		t.Fatal("unmanaged setting readable")
	}
	if err := SetSetting("signal", " sig.example.net "); err != nil {
		t.Fatal(err)
	}
	fp, err := Identity()
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _ := ed25519.GenerateKey(nil)
	if err := SetSetting("signal-pin", auth.EncodeKey(pub)); err != nil {
		t.Fatal(err)
	}
	if err := SetSetting("signal", "sig.example.net"); err != nil {
		t.Fatal(err)
	}
	if v, _ := GetSetting("signal-pin"); v == "" {
		t.Fatal("re-setting the same server dropped its pin")
	}
	// A new address drops the old address's pin.
	if err := SetSetting("signal", "other.example.net:21116"); err != nil {
		t.Fatal(err)
	}
	if v, _ := GetSetting("signal-pin"); v != "" {
		t.Fatalf("pin %q survived a change of rendezvous server", v)
	}
	if err := SetSetting("name", "Pixel 9"); err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	js, err := SettingsJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(js), &m); err != nil {
		t.Fatal(err)
	}
	if m["signal"] != "other.example.net:21116" || m["relay"] != "on" || m["name"] != "Pixel 9" || m["fingerprint"] != fp {
		t.Fatalf("settings %v", m)
	}
	if line, err := PublicKeyLine(); err != nil || !strings.Contains(line, "Pixel 9") {
		t.Fatalf("key line %q, %v", line, err)
	}
	b, err := os.ReadFile(filepath.Join(d, "config.json"))
	if err != nil || !bytes.Contains(b, []byte("other.example.net")) {
		t.Fatalf("settings not stored in the app's directory: %s %v", b, err)
	}
	if err := SetSetting("signal", ""); err != nil {
		t.Fatal(err)
	}
	if v, _ := GetSetting("signal"); v != "" {
		t.Fatalf("signal = %q after clearing", v)
	}
}

func TestGatewayLifecycle(t *testing.T) {
	initDir(t)
	u1, err := StartGateway()
	if err != nil {
		t.Fatal(err)
	}
	pu, err := url.Parse(u1)
	if err != nil {
		t.Fatal(err)
	}
	if pu.Scheme != "http" || pu.Hostname() != "127.0.0.1" || len(strings.Trim(pu.Path, "/")) < 20 {
		t.Fatalf("gateway URL %q is not loopback with a token", u1)
	}
	if !GatewayRunning() {
		t.Fatal("not running")
	}
	if u, _ := StartGateway(); u != u1 {
		t.Fatal("a second StartGateway changed the URL of the running gateway")
	}
	get := func(u string, host string) int {
		req, _ := http.NewRequest("GET", u, nil)
		if host != "" {
			req.Host = host
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := get(u1, ""); c != 200 {
		t.Fatalf("page: %d", c)
	}
	if c := get("http://"+pu.Host+"/", ""); c != 404 {
		t.Fatalf("page without token: %d", c)
	}
	if c := get(u1, "evil.example:"+pu.Port()); c != 403 {
		t.Fatalf("foreign Host header: %d", c)
	}
	StopGateway()
	if GatewayRunning() {
		t.Fatal("still running after stop")
	}
	u2, err := StartGateway()
	if err != nil {
		t.Fatal(err)
	}
	if u2 == u1 {
		t.Fatal("restart reused the token")
	}
	if c := get(u1, ""); c == 200 {
		t.Fatal("old URL still works")
	}
}

func TestSetLocalAddresses(t *testing.T) {
	SetLocalAddresses("192.168.1.23/24, fe80::1%wlan0 127.0.0.1 2001:db8::5/64 junk 10.0.0.2")
	localsMu.Lock()
	got := append([]netip.Addr(nil), nativeLAN...)
	localsMu.Unlock()
	want := []string{"192.168.1.23", "10.0.0.2", "2001:db8::5"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i].String() != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	SetLocalAddresses("")
}

type pairResult struct {
	OK           bool   `json:"ok"`
	Code         string `json:"code"`
	Error        string `json:"error"`
	Target       string `json:"target"`
	Alias        string `json:"alias"`
	HostIdentity string `json:"host_identity"`
	NewHost      bool   `json:"new_host"`
}

func pairJSON(t *testing.T, target, pw, alias string) pairResult {
	t.Helper()
	js, err := PairWithPassword(target, pw, alias)
	if err != nil {
		t.Fatal(err)
	}
	var r pairResult
	if err := json.Unmarshal([]byte(js), &r); err != nil {
		t.Fatalf("%s: %v", js, err)
	}
	return r
}

// TestPairThenConnectThroughGateway is the app's whole path without the
// WebView: pair with the host's password through the native API, then let
// the page (here: plain HTTP calls, as app.js makes them) connect with the
// saved pairing alone and use a feature.
func TestPairThenConnectThroughGateway(t *testing.T) {
	initDir(t)
	SetDeviceName("Test Phone")
	addr := startHost(t)

	if r := pairJSON(t, addr, "wrong password!", ""); r.OK || r.Code != "bad_password" {
		t.Fatalf("wrong password: %+v", r)
	}
	if r := pairJSON(t, "", "x", ""); r.OK || r.Code != "bad_request" {
		t.Fatalf("empty target: %+v", r)
	}
	r := pairJSON(t, addr, hostPassword, "lab")
	if !r.OK || r.Target != addr || r.Alias != "lab" || r.HostIdentity == "" {
		t.Fatalf("pair: %+v", r)
	}

	js, err := DevicesJSON(false)
	if err != nil {
		t.Fatal(err)
	}
	var devs []struct{ Target, Alias, Login, Presence string }
	if err := json.Unmarshal([]byte(js), &devs); err != nil {
		t.Fatal(err)
	}
	if len(devs) != 1 || devs[0].Alias != "lab" || devs[0].Login != "pairing" || devs[0].Presence != "not_checked" {
		t.Fatalf("devices: %s", js)
	}

	gwURL, err := StartGateway()
	if err != nil {
		t.Fatal(err)
	}
	c := &pageClient{t: t, base: gwURL}
	var sess struct{ SID, CSRF string }
	c.call("POST", "session", map[string]string{"sid": ""}, &sess)
	if sess.SID == "" || sess.CSRF == "" {
		t.Fatalf("session: %+v", sess)
	}
	c.sid, c.csrf = sess.SID, sess.CSRF

	var peers struct {
		Peers []struct {
			Target, Alias string
			Paired        bool
		}
	}
	c.call("GET", "peers", nil, &peers)
	if len(peers.Peers) != 1 || !peers.Peers[0].Paired {
		t.Fatalf("peers: %+v", peers)
	}

	var att struct {
		Attempt string
		State   string
		Error   *struct{ Code, Message string }
		Conn    *struct{ Key, Auth string }
	}
	c.call("POST", "connect", map[string]any{"target": "lab"}, &att)
	deadline := time.Now().Add(20 * time.Second)
	for att.State != "connected" {
		if att.State == "failed" || att.State == "canceled" || time.Now().After(deadline) {
			t.Fatalf("connect: %+v %+v", att, att.Error)
		}
		time.Sleep(100 * time.Millisecond)
		c.call("GET", "connect/"+att.Attempt, nil, &att)
	}
	if att.Conn.Auth != "token" && att.Conn.Auth != "pairing" {
		t.Logf("logged in with %q", att.Conn.Auth)
	}
	var list struct {
		Path    string
		Entries []struct{ Name string }
	}
	c.call("GET", "files/list?c="+url.QueryEscape(att.Conn.Key)+"&path="+url.QueryEscape(t.TempDir()), nil, &list)
	if list.Path == "" {
		t.Fatalf("files/list: %+v", list)
	}
	// A pairing made from the page uses this device's label, not the
	// process hostname.
	if l := pairLabel(); l != "Test Phone" {
		t.Fatalf("pair label %q", l)
	}
}

type pageClient struct {
	t         *testing.T
	base      string
	sid, csrf string
}

func (c *pageClient) call(method, path string, body, out any) {
	c.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+"api/v1/"+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.sid != "" {
		req.Header.Set("X-RemoHop-Session", c.sid)
		req.Header.Set("X-RemoHop-CSRF", c.csrf)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		c.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
}
