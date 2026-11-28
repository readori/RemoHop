package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

type rig struct {
	*client
	d   *dialer
	key string
}

func newRig(t *testing.T, o peertest.Options) *rig {
	t.Helper()
	return newRigServing(t, o, nil)
}

// newRigServing is newRig with hosts that serve services (nil:
// peer.HostServices).
func newRigServing(t *testing.T, o peertest.Options, services *peer.Registry) *rig {
	t.Helper()
	testEnv(t)
	d := &dialer{t: t, opts: o, services: services}
	s := startServer(t, Config{Dial: d.dial, Peers: func() ([]PeerInfo, error) {
		return []PeerInfo{{Target: "123456789", Alias: "box", Paired: true}}, nil
	}})
	c := newLoopbackClient(t, s)
	c.newSession()
	return &rig{client: c, d: d, key: c.connect("123 456 789", "hostpw")}
}

func (r *rig) q(params ...string) string {
	v := url.Values{"c": {r.key}}
	for i := 0; i+1 < len(params); i += 2 {
		v.Set(params[i], params[i+1])
	}
	return v.Encode()
}

func TestConnectFlowAndErrors(t *testing.T) {
	testEnv(t)
	d := &dialer{t: t}
	s := startServer(t, Config{Dial: d.dial, MaxSessions: 1})
	c := newLoopbackClient(t, s)
	c.newSession()

	// wrong password: a failed attempt with the host's refusal
	var a map[string]any
	c.jsonCall("POST", "api/v1/connect", map[string]any{"target": "123-456-789", "password": "nope"}, &a)
	var j map[string]any
	for i := 0; i < 200; i++ {
		c.jsonCall("GET", "api/v1/connect/"+a["attempt"].(string), nil, &j)
		if j["state"] == "failed" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if j["state"] != "failed" || j["error"].(map[string]any)["code"] != "host_refused" {
		t.Fatalf("failed attempt: %v", j)
	}
	if got := d.calls[0].Target; got != "123456789" {
		t.Errorf("target not normalised: %q", got)
	}
	// the approval notice becomes a state while dialling and is kept
	if n, _ := j["notices"].([]any); len(n) == 0 {
		t.Error("no notices")
	}
	// bad target
	if code := c.jsonCall("POST", "api/v1/connect", map[string]any{"target": ""}, nil); code != 400 {
		t.Errorf("empty target: %d", code)
	}
	key := c.connect("123456789", "hostpw")
	var conns struct{ Conns []connInfo }
	c.jsonCall("GET", "api/v1/connections", nil, &conns)
	if len(conns.Conns) != 1 || conns.Conns[0].Key != key {
		t.Fatalf("connections: %+v", conns)
	}
	// the slot limit
	var e struct{ Error apiError }
	if code := c.jsonCall("POST", "api/v1/connect", map[string]any{"target": "987654321", "password": "hostpw"}, &e); code != 503 || e.Error.Code != "too_many_sessions" {
		t.Errorf("over the limit: %d %+v", code, e)
	}
	// disconnect frees the slot
	c.jsonCall("POST", "api/v1/disconnect", map[string]any{"c": key}, nil)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if code := c.jsonCall("POST", "api/v1/connect", map[string]any{"target": "987654321", "password": "hostpw"}, nil); code == 202 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("slot not released")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// a feature on a dropped connection says so
	if code, b := c.do("GET", "api/v1/info?c="+key, nil, nil); code != 410 {
		t.Errorf("info on a closed session: %d %s", code, b)
	}
}

func TestPasswordNotKeptOrLogged(t *testing.T) {
	testEnv(t)
	d := &dialer{t: t}
	s := startServer(t, Config{Dial: d.dial})
	c := newLoopbackClient(t, s)
	c.newSession()
	c.connect("123456789", "hostpw")
	if len(d.calls) != 1 || d.calls[0].Password != "hostpw" {
		t.Fatalf("dial calls %+v", d.calls)
	}
	for _, ws := range s.sessions {
		for _, a := range ws.attempts {
			if strings.Contains(strings.Join(a.snapshot()["notices"].([]string), ""), "hostpw") {
				t.Error("password in notices")
			}
		}
	}
}

func TestNoSavedMode(t *testing.T) {
	testEnv(t)
	d := &dialer{t: t}
	s := startServer(t, Config{Dial: d.dial, NoSaved: true, Peers: func() ([]PeerInfo, error) { return []PeerInfo{{Target: "1"}}, nil }})
	c := newLoopbackClient(t, s)
	c.newSession()
	var p struct{ Peers []PeerInfo }
	c.jsonCall("GET", "api/v1/peers", nil, &p)
	if len(p.Peers) != 0 {
		t.Error("saved peers exposed")
	}
	var e struct{ Error apiError }
	if code := c.jsonCall("POST", "api/v1/connect", map[string]any{"target": "123456789"}, &e); code != 401 || e.Error.Code != "password_required" {
		t.Errorf("connect without password: %d %+v", code, e)
	}
}

func TestPairFlow(t *testing.T) {
	testEnv(t)
	d := &dialer{t: t}
	var paired string
	s := startServer(t, Config{Dial: d.dial, Pair: func(_ context.Context, c *peer.Conn, name string) error { paired = name; return nil }})
	c := newLoopbackClient(t, s)
	c.newSession()
	var a map[string]any
	c.jsonCall("POST", "api/v1/connect", map[string]any{"target": "123456789", "password": "hostpw", "pair": true, "pair_name": "laptop"}, &a)
	for i := 0; i < 500; i++ {
		var j map[string]any
		c.jsonCall("GET", "api/v1/connect/"+a["attempt"].(string), nil, &j)
		if j["state"] == "connected" {
			if j["conn"].(map[string]any)["paired"] != true {
				t.Errorf("not marked paired: %v", j)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if paired != "laptop" {
		t.Errorf("pair name %q", paired)
	}
}

// A host that refuses pairing (remohop host --no-pairing, quick support):
// the session works, the page gets a stable code for the refusal and the
// connection is not marked paired.
func TestPairRefused(t *testing.T) {
	testEnv(t)
	d := &dialer{t: t}
	s := startServer(t, Config{Dial: d.dial, Pair: func(context.Context, *peer.Conn, string) error {
		return errors.New("pair: this host does not allow pairing")
	}})
	c := newLoopbackClient(t, s)
	c.newSession()
	var a map[string]any
	c.jsonCall("POST", "api/v1/connect", map[string]any{"target": "123456789", "password": "hostpw", "pair": true}, &a)
	for i := 0; ; i++ {
		var j map[string]any
		c.jsonCall("GET", "api/v1/connect/"+a["attempt"].(string), nil, &j)
		if j["state"] == "connected" {
			if j["conn"].(map[string]any)["paired"] == true {
				t.Errorf("marked paired although the host refused: %v", j)
			}
			p, _ := j["pair"].(map[string]any)
			if p == nil || p["ok"] != false || p["code"] != "pair_refused" || p["reason"] != "no_pairing" {
				t.Errorf("pair outcome: %v", j["pair"])
			}
			break
		}
		if i > 500 {
			t.Fatalf("not connected: %v", j)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := pairFailure(errors.New("pairing is not allowed by grant support-1")); got.Code != "pair_refused" || got.Reason != "grant" {
		t.Errorf("grant: %+v", got)
	}
	if got := pairFailure(errors.New("the host could not store the pairing")); got.Code != "pair_failed" || got.Reason != "" {
		t.Errorf("other: %+v", got)
	}
}

// Remembering the login of a host that does not pair saves its password
// instead; the page can show it and remove it, and a later pairing makes
// it unnecessary.
func TestPairRefusedSavesPassword(t *testing.T) {
	testEnv(t)
	d := &dialer{t: t}
	saved := map[string]string{}
	s := startServer(t, Config{Dial: d.dial,
		Pair: func(context.Context, *peer.Conn, string) error {
			return errors.New("pair: this host does not allow pairing")
		},
		SavePassword: func(target, pw string) error {
			if pw == "" {
				delete(saved, target)
			} else {
				saved[target] = pw
			}
			return nil
		},
		Password: func(target string) (string, error) {
			if pw, ok := saved[target]; ok {
				return pw, nil
			}
			return "", errNoSuchPeer
		},
	})
	c := newLoopbackClient(t, s)
	c.newSession()
	var a map[string]any
	c.jsonCall("POST", "api/v1/connect", map[string]any{"target": "123456789", "password": "hostpw", "pair": true}, &a)
	for i := 0; ; i++ {
		var j map[string]any
		c.jsonCall("GET", "api/v1/connect/"+a["attempt"].(string), nil, &j)
		if j["state"] == "connected" {
			p, _ := j["pair"].(map[string]any)
			if p == nil || p["ok"] != true || p["code"] != "password_saved" {
				t.Errorf("pair outcome: %v", j["pair"])
			}
			for _, n := range j["notices"].([]any) {
				if strings.Contains(n.(string), "pairing failed") {
					t.Errorf("a saved password is reported as a failure: %v", n)
				}
			}
			break
		}
		if i > 500 {
			t.Fatalf("not connected: %v", j)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if saved["123456789"] != "hostpw" {
		t.Fatalf("saved: %v", saved)
	}
	var r map[string]any
	c.jsonCall("POST", "api/v1/peers/password", map[string]any{"target": "123 456 789"}, &r)
	if r["password"] != "hostpw" {
		t.Fatalf("reveal: %v", r)
	}
	c.jsonCall("POST", "api/v1/peers/password", map[string]any{"target": "123456789", "forget": true}, &r)
	if _, ok := saved["123456789"]; ok {
		t.Fatal("forget kept the password")
	}
	if code := c.jsonCall("POST", "api/v1/peers/password", map[string]any{"target": "123456789"}, &r); code != http.StatusNotFound {
		t.Fatalf("reveal after forget: HTTP %d", code)
	}
}

func TestFilesAPI(t *testing.T) {
	r := newRig(t, peertest.Options{})
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "b.txt"), []byte("bee"), 0o644)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("ay"), 0o644)
	os.Mkdir(filepath.Join(root, "zdir"), 0o755)
	if runtime.GOOS != "windows" { // Windows forbids control characters and quotes in names
		os.WriteFile(filepath.Join(root, "bad\x1b[31mname"), []byte("x"), 0o644)
	}
	bigName, wantCD := "big \"q\" é.bin", "filename*=utf-8''big%20_q_%20%C3%A9.bin"
	if runtime.GOOS == "windows" {
		bigName, wantCD = "big q é.bin", "filename*=utf-8''big%20q%20%C3%A9.bin"
	}
	hasLink := os.Symlink("zdir", filepath.Join(root, "link")) == nil // may fail without privilege on Windows

	var l struct {
		Path, Parent, Sep string
		Entries           []entry
	}
	if code := r.jsonCall("GET", "api/v1/files/list?"+r.q("path", root), nil, &l); code != 200 {
		t.Fatalf("list: %d", code)
	}
	var names []string
	for _, e := range l.Entries {
		names = append(names, e.Display)
		if strings.ContainsAny(e.Display, "\x1b") {
			t.Errorf("control character in display name %q", e.Display)
		}
	}
	if len(l.Entries) != len(mustReadDir(t, root)) || !l.Entries[0].Dir || (hasLink && !l.Entries[1].Dir) || l.Parent != filepath.Dir(l.Path) {
		t.Errorf("listing %v parent %q", names, l.Parent)
	}
	var link entry
	for _, e := range l.Entries {
		if e.Name == "link" {
			link = e
		}
	}
	if hasLink && (!link.Link || !link.Dir) {
		t.Errorf("symlink to a directory: %+v", link)
	}
	// home when no path is given
	if code := r.jsonCall("GET", "api/v1/files/list?"+r.q(), nil, &l); code != 200 || l.Path == "" {
		t.Errorf("home listing: %d %q", code, l.Path)
	}
	var e struct{ Error apiError }
	if code := r.jsonCall("GET", "api/v1/files/list?"+r.q("path", filepath.Join(root, "nope")), nil, &e); code != 404 || e.Error.Code != "not_found" {
		t.Errorf("missing dir: %d %+v", code, e)
	}

	// download: bytes, headers, ticket-authenticated
	big := make([]byte, 3<<20+17)
	rand.Read(big)
	os.WriteFile(filepath.Join(root, bigName), big, 0o644)
	dl := func(path string) (*http.Response, []byte) {
		var tk struct{ Ticket string }
		r.jsonCall("POST", "api/v1/ticket", map[string]any{"purpose": "dl"}, &tk)
		resp, err := http.Get(r.base + "api/v1/files/download?" + r.q("path", path, "t", tk.Ticket))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp, b
	}
	resp, body := dl(filepath.Join(root, bigName))
	if resp.StatusCode != 200 || !bytes.Equal(body, big) {
		t.Fatalf("download: %d, %d bytes: %.300s", resp.StatusCode, len(body), body)
	}
	cd := resp.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, "attachment;") || !strings.Contains(cd, wantCD) {
		t.Errorf("Content-Disposition %q", cd)
	}
	if strings.Contains(cd, "\r") || strings.Contains(cd, "\n") {
		t.Error("header injection")
	}
	if resp.Header.Get("Content-Type") != "application/octet-stream" || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Error("download content type headers")
	}
	if resp, _ := dl(root); resp.StatusCode != 400 {
		t.Errorf("download of a directory: %d", resp.StatusCode)
	}
	if resp, _ := dl(filepath.Join(root, "missing")); resp.StatusCode != 404 {
		t.Errorf("download of a missing file: %d", resp.StatusCode)
	}
	// a ticket is single use
	var tk struct{ Ticket string }
	r.jsonCall("POST", "api/v1/ticket", map[string]any{"purpose": "dl"}, &tk)
	u := r.base + "api/v1/files/download?" + r.q("path", filepath.Join(root, "a.txt"), "t", tk.Ticket)
	for i, want := range []int{200, 401} {
		resp, _ := http.Get(u)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("download attempt %d: %d, want %d", i, resp.StatusCode, want)
		}
	}

	// upload streams the body; a clash renames instead of replacing
	up := func(name, overwrite string, data []byte) (int, uploadResult) {
		var res uploadResult
		path := "api/v1/files/upload?" + r.q("dir", root, "name", name, "overwrite", overwrite)
		code, b := r.do("POST", path, data, nil)
		json.Unmarshal(b, &res)
		return code, res
	}
	code, res := up("new.bin", "0", big)
	if code != 200 || res.Renamed || res.Size != int64(len(big)) {
		t.Fatalf("upload: %d %+v", code, res)
	}
	got, _ := os.ReadFile(filepath.Join(root, "new.bin"))
	if !bytes.Equal(got, big) {
		t.Error("uploaded bytes differ")
	}
	code, res = up("a.txt", "0", []byte("clash"))
	if code != 200 || !res.Renamed || res.Name == "a.txt" {
		t.Fatalf("clashing upload: %d %+v", code, res)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(b) != "ay" {
		t.Errorf("existing file was replaced: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, res.Name)); string(b) != "clash" {
		t.Errorf("renamed upload content %q", b)
	}
	if code, res = up("a.txt", "1", []byte("replaced")); code != 200 || res.Renamed {
		t.Fatalf("overwrite upload: %d %+v", code, res)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(b) != "replaced" {
		t.Errorf("confirmed overwrite content %q", b)
	}
	for _, bad := range []string{"../x", "a/b", `a\b`, "..", ".", "", "x\x00y", strings.Repeat("n", 300)} {
		if code, _ := up(bad, "0", []byte("x")); code != 400 {
			t.Errorf("upload name %q: %d, want 400", bad, code)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(root), "x")); err == nil {
		t.Error("path traversal wrote outside the directory")
	}
	// nothing was written on the gateway's own disk: it only pipes
	// (the config dir holds no copies)

	// mkdir / rename / delete
	post := func(path string, body any) (int, apiError) {
		var e struct{ Error apiError }
		code := r.jsonCall("POST", path, body, &e)
		return code, e.Error
	}
	if code, e := post("api/v1/files/mkdir", map[string]any{"c": r.key, "path": filepath.Join(root, "newdir")}); code != 200 {
		t.Fatalf("mkdir: %d %+v", code, e)
	}
	if code, e := post("api/v1/files/mkdir", map[string]any{"c": r.key, "path": filepath.Join(root, "newdir")}); code != 409 || e.Code != "exists" {
		t.Errorf("mkdir twice: %d %+v", code, e)
	}
	if code, _ := post("api/v1/files/mkdir", map[string]any{"c": r.key, "path": root + "/x/.."}); code != 400 {
		t.Errorf("mkdir with a bad name: %d", code)
	}
	if code, e := post("api/v1/files/rename", map[string]any{"c": r.key, "from": filepath.Join(root, "b.txt"), "to": filepath.Join(root, "c.txt")}); code != 200 {
		t.Fatalf("rename: %d %+v", code, e)
	}
	if code, e := post("api/v1/files/rename", map[string]any{"c": r.key, "from": filepath.Join(root, "c.txt"), "to": filepath.Join(root, "a.txt")}); code != 409 {
		t.Errorf("rename onto an existing file: %d %+v", code, e)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(b) != "replaced" {
		t.Error("rename replaced a file")
	}
	os.WriteFile(filepath.Join(root, "newdir", "f"), []byte("x"), 0o644)
	if code, e := post("api/v1/files/delete", map[string]any{"c": r.key, "path": filepath.Join(root, "newdir")}); code == 200 {
		t.Errorf("deleting a non-empty directory without recursive: %d %+v", code, e)
	}
	if code, _ := post("api/v1/files/delete", map[string]any{"c": r.key, "path": filepath.Join(root, "newdir"), "recursive": true}); code != 200 {
		t.Errorf("recursive delete: %d", code)
	}
	if code, _ := post("api/v1/files/delete", map[string]any{"c": r.key, "path": filepath.Join(root, "c.txt")}); code != 200 {
		t.Errorf("delete: %d", code)
	}
	if _, err := os.Stat(filepath.Join(root, "c.txt")); !errors.Is(err, os.ErrNotExist) {
		t.Error("file still there")
	}
	if code, _ := post("api/v1/files/delete", map[string]any{"c": r.key, "path": "/"}); code != 400 {
		t.Errorf("delete /: %d", code)
	}
}

func TestPathHelpers(t *testing.T) {
	for in, want := range map[string]string{"/": "", "/a": "/", "/a/b": "/a", "/a/b/": "/a", `C:\`: "", `C:\x`: `C:\`, `C:\x\y`: `C:\x`, "": ""} {
		if got := parentOf(in); got != want && !(in == `C:\` && got == "") {
			t.Errorf("parentOf(%q) = %q, want %q", in, got, want)
		}
	}
	if joinRemote("/a", "b") != "/a/b" || joinRemote("/", "b") != "/b" || joinRemote(`C:\x`, "b") != `C:\x\b` {
		t.Error("joinRemote")
	}
	if baseName(`C:\x\y.txt`) != "y.txt" || baseName("/a/b/") != "b" {
		t.Error("baseName")
	}
	cd := contentDisposition("a\r\nSet-Cookie: x\"/b")
	if strings.ContainsAny(cd, "\r\n") {
		t.Errorf("injection %q", cd)
	}
}

func TestClipboardAndInfo(t *testing.T) {
	r := newRig(t, peertest.Options{})
	var cg struct {
		Kind, Text string
	}
	r.jsonCall("POST", "api/v1/clip/get", map[string]any{"c": r.key}, &cg)
	if cg.Kind != "empty" {
		t.Logf("initial clipboard: %+v", cg)
	}
	if code := r.jsonCall("POST", "api/v1/clip/set", map[string]any{"c": r.key, "text": "héllo clip\nline2"}, nil); code != 200 {
		t.Fatalf("clip set: %d", code)
	}
	if code := r.jsonCall("POST", "api/v1/clip/get", map[string]any{"c": r.key}, &cg); code != 200 || cg.Kind != "text" || cg.Text != "héllo clip\nline2" {
		t.Fatalf("clip get: %d %+v", code, cg)
	}
	if code := r.jsonCall("POST", "api/v1/clip/set", map[string]any{"c": r.key, "text": ""}, nil); code != 400 {
		t.Errorf("empty clip set: %d", code)
	}
	var info struct {
		Info map[string]any
		Conn connInfo
	}
	if code := r.jsonCall("GET", "api/v1/info?"+r.q(), nil, &info); code != 200 || info.Info["hostname"] == "" || info.Info["os"] == nil {
		t.Fatalf("info: %d %+v", code, info)
	}
	// screenshot on a headless test machine fails cleanly (no X server)
	code, b := r.do("GET", "api/v1/screenshot?"+r.q(), nil, nil)
	if code == 200 {
		if !bytes.HasPrefix(b, []byte("\x89PNG")) {
			t.Error("screenshot is not a PNG")
		}
	} else if !strings.Contains(string(b), `"error"`) {
		t.Errorf("screenshot error body %s", b)
	}
}

func TestHostRefusalSurfacesVerbatim(t *testing.T) {
	const why = "the host does not allow fs (feature \"file\" is switched off; its owner can allow it with `remohop config feature file on`)"
	r := newRig(t, peertest.Options{Authorize: func(service string) error {
		if service == "fs" || service == "clip" || service == "shell" {
			return errors.New(why)
		}
		return nil
	}})
	for _, c := range []struct{ method, path string }{
		{"GET", "api/v1/files/list?" + r.q()},
		{"POST", "api/v1/clip/get"},
	} {
		var body any
		if c.method == "POST" {
			body = map[string]any{"c": r.key}
		}
		var e struct{ Error apiError }
		code := r.jsonCall(c.method, c.path, body, &e)
		if code != 403 || e.Error.Code != "feature_off" || !strings.Contains(e.Error.Message, "switched off") || !strings.Contains(e.Error.Message, "remohop config feature file on") {
			t.Errorf("%s: %d %+v", c.path, code, e)
		}
	}
	// the terminal reports it on the WebSocket
	ws := r.dialWS(t, "term/ws", r.q("rows", "24", "cols", "80"))
	defer ws.Close()
	m := readControl(t, ws, "refused")
	if !strings.Contains(m["message"].(string), "switched off") {
		t.Errorf("terminal refusal %v", m)
	}
}

func (r *rig) dialWS(t *testing.T, path, query string) *websocket.Conn {
	t.Helper()
	var tk struct{ Ticket string }
	r.jsonCall("POST", "api/v1/ticket", map[string]any{"purpose": "ws"}, &tk)
	u, _ := url.Parse(r.base)
	wsu := "ws://" + u.Host + u.Path + "api/v1/" + path + "?" + query + "&t=" + tk.Ticket
	cfg, err := websocket.NewConfig(wsu, "http://"+u.Host)
	if err != nil {
		t.Fatal(err)
	}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		t.Fatalf("dial %s: %v", path, err)
	}
	return ws
}

// readControl reads messages until a text message of the given type.
func readControl(t *testing.T, ws *websocket.Conn, typ string) map[string]any {
	t.Helper()
	for {
		ws.SetReadDeadline(time.Now().Add(15 * time.Second))
		var msg []byte
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			t.Fatalf("waiting for %q: %v", typ, err)
		}
		var m map[string]any
		if json.Unmarshal(msg, &m) == nil && m["type"] == typ {
			return m
		}
	}
}

// readUntil reads terminal output until it contains want.
func readUntil(t *testing.T, ws *websocket.Conn, want string) string {
	t.Helper()
	var out strings.Builder
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		ws.SetReadDeadline(deadline)
		var msg []byte
		if err := websocket.Message.Receive(ws, &msg); err != nil {
			t.Fatalf("waiting for %q, got %q: %v", want, out.String(), err)
		}
		if len(msg) > 0 && msg[0] != '{' {
			out.Write(msg)
		}
		if strings.Contains(out.String(), want) {
			return out.String()
		}
	}
	t.Fatalf("timed out waiting for %q, got %q", want, out.String())
	return ""
}

func sendTerm(t *testing.T, ws *websocket.Conn, v any) {
	t.Helper()
	b, _ := json.Marshal(v)
	if err := websocket.Message.Send(ws, string(b)); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalBridge(t *testing.T) {
	r := newRig(t, peertest.Options{})
	// Shell sessions outlive the host of one test (they live in a
	// process-wide table): end this one's, or -count=2 finds it there.
	t.Cleanup(func() {
		r.jsonCall("POST", "api/v1/term/kill", map[string]any{"c": r.key, "name": "web1"}, nil)
	})
	ws := r.dialWS(t, "term/ws", r.q("rows", "24", "cols", "80", "session", "web1"))
	defer ws.Close()
	att := readControl(t, ws, "attached")
	if att["session"] != "web1" || att["created"] != true {
		t.Fatalf("attached %v", att)
	}
	sendTerm(t, ws, map[string]any{"type": "in", "data": "echo hello-$((6*7))\r"})
	readUntil(t, ws, "hello-42")
	sendTerm(t, ws, map[string]any{"type": "resize", "rows": 31, "cols": 101})
	time.Sleep(200 * time.Millisecond)
	sendTerm(t, ws, map[string]any{"type": "in", "data": "stty size\r"})
	readUntil(t, ws, "31 101")

	// sessions list, detach by closing, re-attach with replay, kill
	var ls struct{ Sessions []struct{ Name string } }
	if code := r.jsonCall("GET", "api/v1/term/sessions?"+r.q(), nil, &ls); code != 200 || len(ls.Sessions) != 1 || ls.Sessions[0].Name != "web1" {
		t.Fatalf("sessions: %d %+v", code, ls)
	}
	ws.Close()
	time.Sleep(300 * time.Millisecond)
	ws2 := r.dialWS(t, "term/ws", r.q("rows", "24", "cols", "80", "session", "web1"))
	defer ws2.Close()
	if att := readControl(t, ws2, "attached"); att["created"] != false {
		t.Errorf("re-attach: %v", att)
	}
	readUntil(t, ws2, "hello-42") // replay of earlier output
	ws3 := r.dialWS(t, "term/ws", r.q("rows", "24", "cols", "80", "new", "1", "session", "second"))
	defer ws3.Close()
	readControl(t, ws3, "attached")
	r.jsonCall("GET", "api/v1/term/sessions?"+r.q(), nil, &ls)
	if len(ls.Sessions) != 2 {
		t.Errorf("sessions after new: %+v", ls)
	}
	// a second client on the same session takes it over
	ws4 := r.dialWS(t, "term/ws", r.q("rows", "24", "cols", "80", "session", "web1"))
	defer ws4.Close()
	readControl(t, ws4, "attached")
	readControl(t, ws2, "detached")
	if code := r.jsonCall("POST", "api/v1/term/kill", map[string]any{"c": r.key, "name": "second"}, nil); code != 200 {
		t.Errorf("kill: %d", code)
	}
	readControl(t, ws3, "exit")
	if code := r.jsonCall("POST", "api/v1/term/kill", map[string]any{"c": r.key, "name": "bad name;"}, nil); code == 200 {
		t.Error("kill accepted a bad name")
	}
}

func TestEmulatorJS(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; the emulator table tests run under Node")
	}
	out, err := exec.Command(node, "testdata/term_core_test.js").CombinedOutput()
	if err != nil {
		t.Fatalf("terminal emulator tests failed:\n%s", out)
	}
}

func TestNoBidiOrControlCharsInAssets(t *testing.T) {
	for _, dir := range []string{"assets", "testdata", "."} {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			b, _ := os.ReadFile(filepath.Join(dir, e.Name()))
			for _, r := range string(b) {
				if r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069 || r == 0x200e || r == 0x200f || r == 0x061c {
					t.Errorf("%s/%s contains the bidi control U+%04X; write it as an escape", dir, e.Name(), r)
					break
				}
			}
		}
	}
}

func mustReadDir(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ents
}
