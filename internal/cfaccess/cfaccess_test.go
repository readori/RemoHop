package cfaccess

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/cfaccess/cfaccesstest"
)

const (
	testTeam = "example-team.cloudflareaccess.com"
	testAUD  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func newTestVerifier(t *testing.T, ks *cfaccesstest.Keys, emails ...string) (*Verifier, *atomic.Int32) {
	t.Helper()
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		if r.URL.Path != "/cdn-cgi/access/certs" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(ks.JWKS())
	}))
	t.Cleanup(srv.Close)
	v, err := New(Config{Team: testTeam, AUD: testAUD, Emails: emails, CertsURL: srv.URL + "/cdn-cgi/access/certs"})
	if err != nil {
		t.Fatal(err)
	}
	return v, &fetches
}

func good() map[string]any {
	now := time.Now().Unix()
	return map[string]any{"aud": []string{testAUD}, "iss": "https://" + testTeam, "exp": now + 600, "iat": now - 10,
		"nbf": now - 10, "email": "Ops@Example.com", "sub": "u1"}
}

func TestVerify(t *testing.T) {
	ks := cfaccesstest.New(t)
	v, fetches := newTestVerifier(t, ks, "ops@example.com")
	ctx := context.Background()
	c, err := v.Verify(ctx, ks.Sign(t, "k1", good()))
	if err != nil {
		t.Fatalf("a good token was refused: %v", err)
	}
	if c.Email != "ops@example.com" {
		t.Fatalf("email %q, want it lower-cased", c.Email)
	}
	// One string as aud is as good as a list.
	cl := good()
	cl["aud"] = testAUD
	if _, err := v.Verify(ctx, ks.Sign(t, "k1", cl)); err != nil {
		t.Fatalf("aud as a string: %v", err)
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("keys fetched %d times, want once (cached)", n)
	}
}

func TestVerifyRefuses(t *testing.T) {
	ks := cfaccesstest.New(t)
	v, _ := newTestVerifier(t, ks, "ops@example.com")
	ctx := context.Background()
	now := time.Now().Unix()
	with := func(k string, val any) map[string]any {
		c := good()
		if val == nil {
			delete(c, k)
		} else {
			c[k] = val
		}
		return c
	}
	cases := []struct {
		name  string
		token string
	}{
		{"wrong aud", ks.Sign(t, "k1", with("aud", []string{"another-app"}))},
		{"wrong issuer", ks.Sign(t, "k1", with("iss", "https://evil.cloudflareaccess.com"))},
		{"expired", ks.Sign(t, "k1", with("exp", now-120))},
		{"not yet valid", ks.Sign(t, "k1", with("nbf", now+600))},
		{"no exp", ks.Sign(t, "k1", with("exp", nil))},
		{"no iat", ks.Sign(t, "k1", with("iat", nil))},
		{"no aud", ks.Sign(t, "k1", with("aud", nil))},
		{"no iss", ks.Sign(t, "k1", with("iss", nil))},
		{"email not allowed", ks.Sign(t, "k1", with("email", "mallory@example.com"))},
		{"no email", ks.Sign(t, "k1", with("email", nil))},
		{"unknown kid", ks.Sign(t, "k-other", good())},
		{"signed by another key", ks.SignWithStranger(t, "k1", good())},
		{"alg none", ks.Unsigned(t, "k1", good())},
		{"garbage", "a.b.c"},
		{"empty", ""},
	}
	for _, c := range cases {
		if _, err := v.Verify(ctx, c.token); err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

func TestSkew(t *testing.T) {
	ks := cfaccesstest.New(t)
	v, _ := newTestVerifier(t, ks)
	now := time.Now().Unix()
	c := good()
	c["exp"] = now - 5 // within the skew
	c["email"] = "anyone@example.com"
	if _, err := v.Verify(context.Background(), ks.Sign(t, "k1", c)); err != nil {
		t.Fatalf("a token a few seconds past exp was refused: %v", err)
	}
}

func TestUnknownKidRefetches(t *testing.T) {
	ks := cfaccesstest.New(t)
	clock := time.Now()
	v, fetches := newTestVerifier(t, ks)
	v.now = func() time.Time { return clock }
	ctx := context.Background()
	if _, err := v.Verify(ctx, ks.Sign(t, "k1", good())); err != nil {
		t.Fatal(err)
	}
	// Cloudflare rotates: a new key appears.
	ks.Rotate(t, "k2")
	clock = clock.Add(time.Minute)
	if _, err := v.Verify(ctx, ks.Sign(t, "k2", good())); err != nil {
		t.Fatalf("a token of a rotated-in key was refused: %v", err)
	}
	if n := fetches.Load(); n != 2 {
		t.Fatalf("keys fetched %d times, want 2", n)
	}
	// Unknown kids in a burst cause no more than one fetch.
	clock = clock.Add(time.Minute)
	for range 10 {
		v.Verify(ctx, ks.Sign(t, "k-forged", good()))
	}
	if n := fetches.Load(); n != 3 {
		t.Fatalf("keys fetched %d times after a burst of unknown kids, want 3", n)
	}
}

func TestNewChecksConfig(t *testing.T) {
	for _, c := range []Config{{Team: "", AUD: "x"}, {Team: "t.example.com", AUD: ""}, {Team: "t.example.com/path", AUD: "x"}} {
		if _, err := New(c); err == nil {
			t.Errorf("New(%+v) accepted", c)
		}
	}
	v, err := New(Config{Team: "https://t.example.com/", AUD: "x"})
	if err != nil || v.Issuer() != "https://t.example.com" {
		t.Fatalf("New with https:// and a slash: %v %v", v, err)
	}
}

func TestRequestHelpers(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if FromCloudflare(r) || TokenFrom(r) != "" {
		t.Fatal("a plain request looks like it came through Cloudflare")
	}
	for _, h := range []string{"Cf-Ray", "Cf-Connecting-Ip", Header} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set(h, "x")
		if !FromCloudflare(r) {
			t.Errorf("%s: not seen as Cloudflare", h)
		}
	}
	r.AddCookie(&http.Cookie{Name: Cookie, Value: "tok"})
	if TokenFrom(r) != "tok" {
		t.Fatal("cookie token not found")
	}
	r.Header.Set(Header, "hdr")
	if TokenFrom(r) != "hdr" {
		t.Fatal("the header should win over the cookie")
	}
}
