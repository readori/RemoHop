package auth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/tls"
	"io"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGeneratePassword(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		p, err := GeneratePassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(p) != passwordLen {
			t.Fatalf("password %q has length %d", p, len(p))
		}
		for _, r := range p {
			if !strings.ContainsRune(passwordAlphabet, r) {
				t.Fatalf("password %q contains %q", p, r)
			}
		}
		if strings.ContainsAny(p, "01lio") {
			t.Fatalf("password %q contains an ambiguous character", p)
		}
		if seen[p] {
			t.Fatalf("password %q generated twice", p)
		}
		seen[p] = true
		if err := CheckPassword(p); err != nil {
			t.Fatalf("generated password rejected: %v", err)
		}
		if got := NormalizePassword(FormatPassword(p)); got != p {
			t.Fatalf("format/normalise round trip: %q -> %q", p, got)
		}
	}
}

func TestNormalizePassword(t *testing.T) {
	for in, want := range map[string]string{
		"abcd-efgh":     "abcdefgh",
		" abcd efgh\n":  "abcdefgh",
		"Correct-Horse": "CorrectHorse",
		"\tpässwörd ":   "pässwörd",
	} {
		if got := NormalizePassword(in); got != want {
			t.Errorf("NormalizePassword(%q) = %q, want %q", in, got, want)
		}
	}
	if FormatPassword("My fixed password") != "My fixed password" {
		t.Error("FormatPassword must leave user-chosen passwords alone")
	}
	if CheckPassword("short") == nil {
		t.Error("CheckPassword accepted a 5-character password")
	}
}

func TestIdentityPersistence(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Public().Equal(b.Public()) {
		t.Fatal("identity changed between loads")
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(filepath.Join(dir, IdentityFile))
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("identity file mode %v, want 0600", st.Mode().Perm())
		}
	}
	key, err := DecodeKey(EncodeKey(a.Public()))
	if err != nil || !key.Equal(a.Public()) {
		t.Fatalf("key encoding round trip failed: %v", err)
	}
	if _, err := DecodeKey("not a key"); err == nil {
		t.Fatal("DecodeKey accepted garbage")
	}
	if !ed25519.Verify(a.Public(), []byte("msg"), a.Sign([]byte("msg"))) {
		t.Fatal("signature does not verify")
	}
	if fp := Fingerprint(a.Public()); len(fp) != 24 || fp != Fingerprint(b.Public()) {
		t.Fatalf("unexpected fingerprint %q", fp)
	}

	os.WriteFile(filepath.Join(dir, "bad.key"), []byte("garbage"), 0o600)
	if _, err := LoadIdentityFile(filepath.Join(dir, "bad.key")); err == nil {
		t.Fatal("corrupt identity file accepted (it must not be silently replaced)")
	}
}

func TestTokenStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), TokensFile)
	s := OpenTokenStore(path)
	if toks, err := s.List(); err != nil || len(toks) != 0 {
		t.Fatalf("empty store: %v, %v", toks, err)
	}
	a, _ := NewIdentity()
	b, _ := NewIdentity()

	t1, err := s.Mint("  work\x1b[31m laptop\n", a.Public())
	if err != nil {
		t.Fatal(err)
	}
	if t1.Name != "work[31m laptop" {
		t.Errorf("name not sanitised: %q", t1.Name)
	}
	if len(t1.Secret) != TokenSize || len(t1.ID) != 16 {
		t.Fatalf("unexpected token shape: id %q, %d secret bytes", t1.ID, len(t1.Secret))
	}
	if runtime.GOOS != "windows" {
		st, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("token file mode %v, want 0600", st.Mode().Perm())
		}
	}
	t2, err := s.Mint("agent", b.Public())
	if err != nil {
		t.Fatal(err)
	}

	// A second OpenTokenStore (another process) sees the same data.
	other := OpenTokenStore(path)
	got, ok, err := other.Lookup(t2.ID)
	if err != nil || !ok || got.Name != "agent" || string(got.Secret) != string(t2.Secret) {
		t.Fatalf("Lookup from second store: %+v %v %v", got, ok, err)
	}

	// Pairing again from the same identity rotates the token.
	t3, err := s.Mint("work laptop", a.Public())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Lookup(t1.ID); ok {
		t.Fatal("old token still valid after re-pairing")
	}
	if toks, _ := s.List(); len(toks) != 2 {
		t.Fatalf("store holds %d tokens, want 2", len(toks))
	}

	if n, err := other.Revoke(t3.ID); err != nil || n != 1 {
		t.Fatalf("Revoke by id = %d, %v", n, err)
	}
	if n, err := other.Revoke("no such"); err != nil || n != 0 {
		t.Fatalf("Revoke of unknown = %d, %v", n, err)
	}
	if _, ok, _ := s.Lookup(t3.ID); ok {
		t.Fatal("revoked token still found")
	}
	if _, ok, _ := s.Lookup(t2.ID); !ok {
		t.Fatal("unrelated token lost on revoke")
	}
}

// tlsPair runs a real TLS 1.3 handshake between two identities over an
// in-memory pipe and returns both connection states.
func tlsPair(t *testing.T, client, server *Identity) (ccs, scs tls.ConnectionState) {
	t.Helper()
	sconf, err := server.ServerTLS("test", true)
	if err != nil {
		t.Fatal(err)
	}
	cconf, err := client.ClientTLS("test", nil)
	if err != nil {
		t.Fatal(err)
	}
	cp, sp := net.Pipe()
	t.Cleanup(func() { cp.Close(); sp.Close() })
	sc, cc := tls.Server(sp, sconf), tls.Client(cp, cconf)
	errc := make(chan error, 1)
	go func() { errc <- sc.Handshake() }()
	if err := cc.Handshake(); err != nil {
		t.Fatalf("client TLS handshake: %v", err)
	}
	// The server's handshake ends only after it has read the client's
	// certificate flight.
	go io.Copy(io.Discard, cc)
	if err := <-errc; err != nil {
		t.Fatalf("server TLS handshake: %v", err)
	}
	return cc.ConnectionState(), sc.ConnectionState()
}

// The binding both ends derive from one real TLS connection is identical,
// and it differs between any two connections, which is what the handshake's
// resistance to relaying rests on.
func TestBindingFromTLS(t *testing.T) {
	client, _ := NewIdentity()
	host, _ := NewIdentity()

	ccs, scs := tlsPair(t, client, host)
	cb, err := ClientBinding(ccs, client.Public())
	if err != nil {
		t.Fatal(err)
	}
	hb, err := HostBinding(scs, host.Public())
	if err != nil {
		t.Fatal(err)
	}
	if len(cb.Exporter) != 32 || !bytes.Equal(cb.Exporter, hb.Exporter) {
		t.Fatal("the two ends of one connection derived different exporters")
	}
	if !cb.ClientKey.Equal(client.Public()) || !cb.HostKey.Equal(host.Public()) ||
		!hb.ClientKey.Equal(client.Public()) || !hb.HostKey.Equal(host.Public()) {
		t.Fatal("binding does not carry the two identity keys")
	}

	// Same two identities, new connection: a different exporter, so a
	// transcript recorded on one connection is useless on another.
	ccs2, _ := tlsPair(t, client, host)
	cb2, err := ClientBinding(ccs2, client.Public())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(cb.Exporter, cb2.Exporter) {
		t.Fatal("two connections share an exporter")
	}

	// An attacker in the middle holds two connections; the victims' views
	// cannot be made to agree.
	attacker, _ := NewIdentity()
	leg1, _ := tlsPair(t, client, attacker)
	_, leg2 := tlsPair(t, attacker, host)
	vb, _ := ClientBinding(leg1, client.Public())
	wb, _ := HostBinding(leg2, host.Public())
	if bytes.Equal(vb.Exporter, wb.Exporter) || vb.HostKey.Equal(wb.HostKey) || vb.ClientKey.Equal(wb.ClientKey) {
		t.Fatal("man-in-the-middle legs produced matching bindings")
	}

	// A server that requires an identity rejects a client without one.
	sconf, _ := host.ServerTLS("test", true)
	cp, sp := net.Pipe()
	defer cp.Close()
	defer sp.Close()
	go func() {
		c := tls.Client(cp, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"test"}})
		c.Handshake()
		io.Copy(io.Discard, c)
	}()
	if err := tls.Server(sp, sconf).Handshake(); err == nil {
		t.Fatal("anonymous client accepted")
	}
}
