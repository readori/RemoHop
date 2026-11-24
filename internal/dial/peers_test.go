package dial

import (
	"strings"
	"testing"
	"time"
)

func TestCheckAlias(t *testing.T) {
	for _, ok := range []string{"lab", "Office-PC", "a.b_c-1", "办公室电脑", "Büro", "Home PC", "家里 的 电脑", "x" + strings.Repeat("y", 62)} {
		if err := CheckAlias(ok); err != nil {
			t.Errorf("CheckAlias(%q) = %v, want ok", ok, err)
		}
	}
	for _, bad := range []string{"", "1lab", "123456789", "host:21118", "a/b", " lab", "lab ", "two  spaces", "-x", "help", "version",
		"x" + strings.Repeat("y", 63), strings.Repeat("电", 64), "tab\there"} {
		if err := CheckAlias(bad); err == nil {
			t.Errorf("CheckAlias(%q) accepted", bad)
		}
	}
}

// Renaming works for paired and recent (key pinned only) entries alike; an
// empty alias removes the name without touching anything else, and adds
// nothing for a host that is not saved.
func TestSetAliasAndClear(t *testing.T) {
	s := mustStore(t, t.TempDir())
	now := time.Now().UTC().Truncate(time.Second)
	if err := s.Update("123456789", func(p *Peer) error { p.HostKey = "k1"; p.TokenID = "t"; p.Token = []byte("x"); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.Update("10.0.0.2:21118", func(p *Peer) error { p.HostKey = "k2"; p.LastSeen = &now; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAlias("123456789", "办公室"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAlias("10.0.0.2:21118", "Home PC"); err != nil {
		t.Fatal(err)
	}
	if p, ok, _ := s.ByAlias("Home PC"); !ok || p.Target != "10.0.0.2:21118" || p.HostKey != "k2" {
		t.Fatalf("ByAlias(Home PC) = %+v, %v", p, ok)
	}
	// Moving an alias takes it from the other host.
	if err := s.SetAlias("10.0.0.2:21118", "办公室"); err != nil {
		t.Fatal(err)
	}
	if p, _, _ := s.Get("123456789"); p.Alias != "" || !p.Paired() {
		t.Fatalf("paired host after its alias moved: %+v", p)
	}
	if err := s.SetAlias("10.0.0.2:21118", ""); err != nil {
		t.Fatalf("clearing an alias: %v", err)
	}
	if p, _, _ := s.Get("10.0.0.2:21118"); p.Alias != "" || p.HostKey != "k2" || p.LastSeen == nil {
		t.Fatalf("after clearing: %+v", p)
	}
	if err := s.SetAlias("999999999", ""); err != nil {
		t.Fatal(err)
	}
	if ps, _ := s.List(); len(ps) != 2 {
		t.Fatalf("clearing the alias of an unknown host added an entry: %+v", ps)
	}
	if err := s.SetAlias("123456789", "bad:name"); err == nil {
		t.Fatal("invalid alias accepted")
	}
	// Forget by target, for a recent entry and a paired one.
	for _, ref := range []string{"10.0.0.2:21118", "123456789"} {
		if n, err := s.Forget(ref); err != nil || n != 1 {
			t.Fatalf("Forget(%s) = %d, %v", ref, n, err)
		}
	}
	if ps, _ := s.List(); len(ps) != 0 {
		t.Fatalf("left after forgetting: %+v", ps)
	}
}
