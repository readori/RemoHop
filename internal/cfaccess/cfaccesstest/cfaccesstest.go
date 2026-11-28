// Package cfaccesstest makes Cloudflare Access tokens for tests: an RSA
// signing key set, its JWKS document and signed JWTs.
package cfaccesstest

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Keys is a set of signing keys by key ID.
type Keys struct {
	mu   sync.Mutex
	keys map[string]*rsa.PrivateKey
}

var (
	poolMu sync.Mutex
	pool   []*rsa.PrivateKey

	strangerOnce sync.Once
	stranger     *rsa.PrivateKey
)

// key returns the i-th test key; RSA key generation is slow, so the keys
// are shared by all tests of a binary.
func key(t testing.TB, i int) *rsa.PrivateKey {
	t.Helper()
	poolMu.Lock()
	defer poolMu.Unlock()
	for len(pool) <= i {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		pool = append(pool, k)
	}
	return pool[i]
}

// New makes a key set with one key, "k1".
func New(t testing.TB) *Keys {
	return &Keys{keys: map[string]*rsa.PrivateKey{"k1": key(t, 0)}}
}

// Rotate adds a key with ID kid.
func (ks *Keys) Rotate(t testing.TB, kid string) {
	ks.mu.Lock()
	n := len(ks.keys)
	ks.mu.Unlock()
	k := key(t, n)
	ks.mu.Lock()
	ks.keys[kid] = k
	ks.mu.Unlock()
}

// JWKS is the key set as https://TEAM/cdn-cgi/access/certs serves it.
func (ks *Keys) JWKS() []byte {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	type jwk struct {
		Kid string `json:"kid"`
		Kty string `json:"kty"`
		Alg string `json:"alg"`
		Use string `json:"use"`
		N   string `json:"n"`
		E   string `json:"e"`
	}
	var set struct {
		Keys []jwk `json:"keys"`
	}
	for kid, k := range ks.keys {
		set.Keys = append(set.Keys, jwk{Kid: kid, Kty: "RSA", Alg: "RS256", Use: "sig",
			N: base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
			E: base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes())})
	}
	b, _ := json.Marshal(set)
	return b
}

// Server serves the key set at /cdn-cgi/access/certs; it returns the URL
// of that document.
func (ks *Keys) Server(t testing.TB) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cdn-cgi/access/certs" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(ks.JWKS())
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/cdn-cgi/access/certs"
}

func encode(t testing.TB, alg, kid string, claims any) string {
	t.Helper()
	h, _ := json.Marshal(map[string]string{"alg": alg, "kid": kid, "typ": "JWT"})
	c, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(c)
}

func sign(t testing.TB, k *rsa.PrivateKey, kid string, claims any) string {
	t.Helper()
	msg := encode(t, "RS256", kid, claims)
	d := sha256.Sum256([]byte(msg))
	sig, err := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, d[:])
	if err != nil {
		t.Fatal(err)
	}
	return msg + "." + base64.RawURLEncoding.EncodeToString(sig)
}

// Sign makes a token signed by the key kid; a kid not in the set is
// signed by the first key but names kid.
func (ks *Keys) Sign(t testing.TB, kid string, claims any) string {
	t.Helper()
	ks.mu.Lock()
	k := ks.keys[kid]
	if k == nil {
		k = ks.keys["k1"]
	}
	ks.mu.Unlock()
	return sign(t, k, kid, claims)
}

// SignWithStranger makes a token that names kid but is signed by a key
// outside the set.
func (ks *Keys) SignWithStranger(t testing.TB, kid string, claims any) string {
	t.Helper()
	strangerOnce.Do(func() {
		var err error
		if stranger, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
			panic(err)
		}
	})
	return sign(t, stranger, kid, claims)
}

// Unsigned makes a token with alg "none".
func (ks *Keys) Unsigned(t testing.TB, kid string, claims any) string {
	t.Helper()
	return encode(t, "none", kid, claims) + "."
}
