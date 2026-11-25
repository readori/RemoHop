package web

import (
	"net/http"
	"testing"
	"time"
)

// Strangers who empty the global failure budget cannot keep out a source
// that logged in before; they still lock out other strangers, and the known
// source keeps its own lockout (security audit finding 8).
func TestLimiterKnownSourceSurvivesGlobalLockout(t *testing.T) {
	now := time.Unix(1000, 0)
	l := newLimiter(func() time.Time { return now })
	if ok, _ := l.allow("owner"); !ok {
		t.Fatal("fresh limiter refuses")
	}
	l.success("owner")
	for i := range 5 * globalBurst {
		l.fail("attacker" + string(rune('A'+i%26)) + string(rune('a'+i/26)))
	}
	if ok, _ := l.allow("stranger"); ok {
		t.Fatal("global failure budget not enforced for strangers")
	}
	if ok, _ := l.allow("owner"); !ok {
		t.Fatal("a known source is locked out by strangers' failures")
	}
	// The known source's own failures still lock it out, and do not
	// spend the global budget.
	tokens := l.tokens
	for range freeFailures + 1 {
		l.fail("owner")
	}
	if ok, _ := l.allow("owner"); ok {
		t.Fatal("a known source is exempt from its own lockout")
	}
	if l.tokens != tokens {
		t.Fatalf("a known source's failures spent the global budget: %v -> %v", tokens, l.tokens)
	}
	// Known only for a while.
	now = now.Add(knownFor + time.Hour)
	for range 5 * globalBurst {
		l.fail("attacker")
		l.per = map[string]*srcState{}
	}
	if ok, _ := l.allow("owner"); ok {
		t.Fatal("a source is known forever")
	}
}

func TestClientKeyRealIPHeader(t *testing.T) {
	pub, _ := parsePublicURL("https://desk.example.com")
	req := func(h http.Header) *http.Request {
		r, _ := http.NewRequest("GET", "http://127.0.0.1:8470/", nil)
		r.Host, r.RemoteAddr, r.Header = "desk.example.com", "127.0.0.1:5555", h
		return r
	}
	off := &Server{public: pub}
	if k := off.clientKey(req(http.Header{"Cf-Connecting-Ip": {"203.0.113.7"}})); k != "127.0.0.1" {
		t.Errorf("CF-Connecting-IP trusted without --real-ip-header: %q", k)
	}
	cf := &Server{public: pub, cfg: Config{RealIPHeader: "CF-Connecting-IP"}}
	if k := cf.clientKey(req(http.Header{"Cf-Connecting-Ip": {"203.0.113.7"}})); k != "203.0.113.7" {
		t.Errorf("CF-Connecting-IP: %q", k)
	}
	xff := &Server{public: pub, cfg: Config{RealIPHeader: "X-Forwarded-For"}}
	if k := xff.clientKey(req(http.Header{"X-Forwarded-For": {"198.51.100.1, 203.0.113.9"}})); k != "203.0.113.9" {
		t.Errorf("X-Forwarded-For takes the address the proxy added: %q", k)
	}
	if k := xff.clientKey(req(http.Header{"X-Forwarded-For": {"junk"}})); k != "127.0.0.1" {
		t.Errorf("unparsable header: %q", k)
	}
	// Not through the public address: the header is ignored.
	r := req(http.Header{"Cf-Connecting-Ip": {"203.0.113.7"}})
	r.Host = "127.0.0.1:8470"
	if k := cf.clientKey(r); k != "127.0.0.1" {
		t.Errorf("header honoured on a direct request: %q", k)
	}
	for _, bad := range []string{"CF Connecting", "x:y", "é"} {
		if checkHeaderName(bad) == nil {
			t.Errorf("header name %q accepted", bad)
		}
	}
	if _, err := New(Config{RealIPHeader: "CF-Connecting-IP"}); err == nil {
		t.Error("--real-ip-header accepted without --public-url")
	}
}
