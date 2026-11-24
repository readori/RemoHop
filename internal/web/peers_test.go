package web

import (
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/dial"
)

// The page names and forgets saved hosts, paired or recent, in the
// gateway's own saved-peers file; forgetting one ends its sessions.
func TestPeerAliasAndForget(t *testing.T) {
	testEnv(t)
	dir := t.TempDir()
	st, err := dial.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	seen := time.Now().UTC().Truncate(time.Second)
	if err := st.Update("123456789", func(p *dial.Peer) error { p.HostKey = "k"; p.TokenID = "t"; p.Token = []byte("x"); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := st.Update("10.0.0.9:21118", func(p *dial.Peer) error { p.HostKey = "k2"; p.LastSeen = &seen; return nil }); err != nil {
		t.Fatal(err)
	}
	d := &dialer{t: t}
	s := startServer(t, Config{Dial: d.dial, ConfigDir: dir})
	c := newLoopbackClient(t, s)
	c.newSession()

	peers := func() map[string]PeerInfo {
		var p struct{ Peers []PeerInfo }
		c.jsonCall("GET", "api/v1/peers", nil, &p)
		m := map[string]PeerInfo{}
		for _, x := range p.Peers {
			m[x.Target] = x
		}
		return m
	}
	if p := peers()["10.0.0.9:21118"]; p.Paired || p.LastSeen == nil || !p.LastSeen.Equal(seen) {
		t.Fatalf("recent host: %+v", p)
	}
	// Rename a recent host and a paired one (an ID typed with spaces).
	for _, tc := range []struct{ target, alias, want string }{
		{"10.0.0.9:21118", " 家里的电脑 ", "家里的电脑"},
		{"123 456 789", "Office PC", "Office PC"},
	} {
		var r map[string]any
		if code := c.jsonCall("POST", "api/v1/peers/alias", map[string]any{"target": tc.target, "alias": tc.alias}, &r); code != 200 {
			t.Fatalf("alias %s: %d %v", tc.target, code, r)
		}
	}
	m := peers()
	if m["10.0.0.9:21118"].Alias != "家里的电脑" || m["123456789"].Alias != "Office PC" || !m["123456789"].Paired {
		t.Fatalf("after renaming: %+v", m)
	}
	// Remove a name.
	if code := c.jsonCall("POST", "api/v1/peers/alias", map[string]any{"target": "123456789", "alias": ""}, nil); code != 200 {
		t.Fatalf("clearing: %d", code)
	}
	if a := peers()["123456789"].Alias; a != "" {
		t.Fatalf("alias not cleared: %q", a)
	}
	// Refusals: a bad name, an unknown host, no target.
	var e struct{ Error apiError }
	if code := c.jsonCall("POST", "api/v1/peers/alias", map[string]any{"target": "123456789", "alias": "a:b"}, &e); code != 400 || e.Error.Code != "bad_alias" {
		t.Errorf("bad alias: %d %+v", code, e)
	}
	if code := c.jsonCall("POST", "api/v1/peers/alias", map[string]any{"target": "987654321", "alias": "x"}, &e); code != 404 {
		t.Errorf("unknown host: %d %+v", code, e)
	}
	if code := c.jsonCall("POST", "api/v1/peers/forget", map[string]any{"target": " "}, &e); code != 400 {
		t.Errorf("no target: %d", code)
	}
	if ps, _ := st.List(); len(ps) != 2 {
		t.Fatalf("refused calls changed the store: %+v", ps)
	}

	// Forget the paired host while connected to it: the session ends.
	key := c.connect("123456789", "hostpw")
	var f struct{ Removed int }
	if code := c.jsonCall("POST", "api/v1/peers/forget", map[string]any{"target": "123456789"}, &f); code != 200 || f.Removed != 1 {
		t.Fatalf("forget: %d %+v", code, f)
	}
	var conns struct{ Conns []connInfo }
	c.jsonCall("GET", "api/v1/connections", nil, &conns)
	for _, x := range conns.Conns {
		if x.Key == key {
			t.Errorf("the forgotten host's session is still there: %+v", x)
		}
	}
	if code := c.jsonCall("POST", "api/v1/peers/forget", map[string]any{"target": "10.0.0.9:21118"}, &f); code != 200 || f.Removed != 1 {
		t.Fatalf("forget recent: %d %+v", code, f)
	}
	if len(peers()) != 0 {
		t.Fatalf("hosts left: %+v", peers())
	}
	// CSRF applies as to every call that changes something.
	if code, _ := c.do("POST", "api/v1/peers/forget", map[string]any{"target": "x:1"}, map[string]string{hdrCSRF: ""}); code != 403 {
		t.Errorf("forget without CSRF: %d", code)
	}
}

func TestPeerEditsOffWithoutSavedHosts(t *testing.T) {
	testEnv(t)
	called := false
	s := startServer(t, Config{Dial: (&dialer{t: t}).dial, NoSaved: true,
		SetAlias: func(string, string) error { called = true; return nil },
		Forget:   func(string) (int, error) { called = true; return 0, nil }})
	c := newLoopbackClient(t, s)
	c.newSession()
	var e struct{ Error apiError }
	for _, p := range []string{"api/v1/peers/alias", "api/v1/peers/forget"} {
		if code := c.jsonCall("POST", p, map[string]any{"target": "123456789", "alias": "x"}, &e); code != 403 || e.Error.Code != "feature_off" {
			t.Errorf("%s on a shared gateway: %d %+v", p, code, e)
		}
	}
	if called {
		t.Error("the store was touched")
	}
}
