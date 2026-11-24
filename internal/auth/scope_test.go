package auth

import (
	"crypto/ed25519"
	"io"
	"path/filepath"
	"testing"
)

// A key nobody authorised may be admitted by ScopedKey, and the result
// carries the scope; an authorised key never asks ScopedKey.
func TestScopedKeyLogin(t *testing.T) {
	cb, hb := testBinding(t)
	store := OpenTokenStore(filepath.Join(t.TempDir(), TokensFile))
	asked := 0
	admit := false
	srv := &Server{Tokens: store, ScopedKey: func(k ed25519.PublicKey) (string, string, bool) {
		asked++
		return "net:laptop", "net", admit && k.Equal(cb.ClientKey)
	}}
	try := func() (*Result, error) {
		res, _, cerr := exchange(t, srv, hb, testSrc, func(rw io.ReadWriter) error {
			return ClientHandshake(rw, cb, KeyCredential(), "")
		})
		return res, cerr
	}
	if _, err := try(); CodeOf(err) != CodeUnknownKey || asked != 1 {
		t.Fatalf("not admitted: %v (asked %d)", err, asked)
	}
	admit = true
	res, err := try()
	if err != nil || res.Scope != "net" || res.TokenName != "net:laptop" || res.TokenID != "" {
		t.Fatalf("scoped login: %+v %v", res, err)
	}
	if _, err := store.Authorize("alice", cb.ClientKey); err != nil {
		t.Fatal(err)
	}
	asked = 0
	res, err = try()
	if err != nil || res.Scope != "" || res.TokenName != "alice" || asked != 0 {
		t.Fatalf("authorised key: %+v %v (asked %d)", res, err, asked)
	}
}
