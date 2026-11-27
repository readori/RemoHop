// Package cfaccess checks Cloudflare Access tokens: the JWT that
// Cloudflare Access puts in the Cf-Access-Jwt-Assertion header (and the
// CF_Authorization cookie) of every request it lets through to an
// application behind a Cloudflare Tunnel.
//
// A token is accepted only if it is signed with RS256 by one of the team's
// current signing keys, published at https://TEAM/cdn-cgi/access/certs; if
// its audience is the application's AUD tag and its issuer is
// https://TEAM; if it carries exp, iat, aud and iss; if it has not expired
// and is already valid (exp and nbf, with a little clock skew); and, when
// an email allowlist is configured, if its email is on the list.
//
// The keys are cached for about an hour. A token whose key ID the cache
// does not know makes the verifier fetch the keys again (Cloudflare rotates
// them), at most once every refetchEvery, so forged key IDs cannot turn
// the verifier into a request amplifier.
package cfaccess

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Header and Cookie carry the token.
const (
	Header = "Cf-Access-Jwt-Assertion"
	Cookie = "CF_Authorization"
)

const (
	cacheFor     = time.Hour
	refetchEvery = 30 * time.Second
	skew         = 30 * time.Second
	maxToken     = 16 << 10
	maxCerts     = 256 << 10
)

// Verifier checks tokens for one Access application.
type Verifier struct {
	team     string // the team domain, e.g. myteam.cloudflareaccess.com
	aud      string
	emails   []string // lower-cased; empty: any email
	certsURL string
	client   *http.Client
	now      func() time.Time

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time // when keys were fetched successfully
	tried   time.Time // the last attempt, successful or not
}

// Config configures a Verifier.
type Config struct {
	// Team is the team domain (myteam.cloudflareaccess.com). A leading
	// https:// and a trailing slash are ignored.
	Team string
	// AUD is the application's Audience (AUD) tag.
	AUD string
	// Emails, if not empty, are the only emails allowed in (compared
	// lower-cased).
	Emails []string

	// CertsURL replaces https://TEAM/cdn-cgi/access/certs (tests).
	CertsURL string
	// Client fetches the keys; default a client with a 10 s timeout.
	Client *http.Client
	// Now replaces the clock (tests).
	Now func() time.Time
}

// New makes a verifier. Team and AUD are required.
func New(c Config) (*Verifier, error) {
	team := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(c.Team), "https://"), "/")
	if team == "" || strings.ContainsAny(team, "/?#@ \t") {
		return nil, fmt.Errorf("cfaccess: %q is not a team domain (like myteam.cloudflareaccess.com)", c.Team)
	}
	if _, err := url.Parse("https://" + team); err != nil {
		return nil, fmt.Errorf("cfaccess: team domain %q: %v", c.Team, err)
	}
	aud := strings.TrimSpace(c.AUD)
	if aud == "" {
		return nil, errors.New("cfaccess: the application's AUD tag is required")
	}
	v := &Verifier{team: team, aud: aud, certsURL: c.CertsURL, client: c.Client, now: c.Now}
	for _, e := range c.Emails {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			v.emails = append(v.emails, e)
		}
	}
	if v.certsURL == "" {
		v.certsURL = "https://" + team + "/cdn-cgi/access/certs"
	}
	if v.client == nil {
		v.client = &http.Client{Timeout: 10 * time.Second}
	}
	if v.now == nil {
		v.now = time.Now
	}
	return v, nil
}

// Issuer is the iss a token must carry.
func (v *Verifier) Issuer() string { return "https://" + v.team }

// Claims are the parts of a verified token callers use.
type Claims struct {
	Email string
	Sub   string
}

// TokenFrom returns the token of a request: the header, else the cookie.
func TokenFrom(r *http.Request) string {
	if t := r.Header.Get(Header); t != "" {
		return t
	}
	if c, err := r.Cookie(Cookie); err == nil {
		return c.Value
	}
	return ""
}

// FromCloudflare reports whether a request came through Cloudflare: it
// carries a header that Cloudflare sets.
func FromCloudflare(r *http.Request) bool {
	return r.Header.Get("Cf-Ray") != "" || r.Header.Get("Cf-Connecting-Ip") != "" || r.Header.Get(Header) != ""
}

type header struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
}

type claims struct {
	Aud   audience `json:"aud"`
	Iss   string   `json:"iss"`
	Exp   *number  `json:"exp"`
	Iat   *number  `json:"iat"`
	Nbf   *number  `json:"nbf"`
	Email string   `json:"email"`
	Sub   string   `json:"sub"`
}

// audience is a JWT aud: one string or a list of them.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*a = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("aud is neither a string nor a list of strings")
	}
	*a = many
	return nil
}

// number is a NumericDate, which may have a fraction.
type number float64

func (n number) time() time.Time {
	sec := float64(n)
	return time.Unix(int64(sec), int64((sec-float64(int64(sec)))*1e9))
}

// ErrNoToken: the request carries no token.
var ErrNoToken = errors.New("no Cloudflare Access token")

// Verify checks a token and returns its claims.
func (v *Verifier) Verify(ctx context.Context, token string) (*Claims, error) {
	if token == "" {
		return nil, ErrNoToken
	}
	if len(token) > maxToken {
		return nil, errors.New("token too long")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("malformed token")
	}
	hb, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, errors.New("malformed token header")
	}
	var h header
	if err := json.Unmarshal(hb, &h); err != nil {
		return nil, errors.New("malformed token header")
	}
	if h.Alg != "RS256" {
		return nil, fmt.Errorf("token algorithm %q, want RS256", h.Alg)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, errors.New("malformed token signature")
	}
	key, err := v.key(ctx, h.Kid)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig); err != nil {
		return nil, errors.New("bad token signature")
	}
	pb, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, errors.New("malformed token payload")
	}
	var c claims
	if err := json.Unmarshal(pb, &c); err != nil {
		return nil, fmt.Errorf("malformed token payload: %v", err)
	}
	switch {
	case c.Exp == nil || c.Iat == nil || len(c.Aud) == 0 || c.Iss == "":
		return nil, errors.New("token lacks exp, iat, aud or iss")
	case c.Iss != v.Issuer():
		return nil, errors.New("token from another issuer")
	case !slices.Contains(c.Aud, v.aud):
		return nil, errors.New("token for another application (aud)")
	}
	now := v.now()
	if !now.Before(c.Exp.time().Add(skew)) {
		return nil, errors.New("token expired")
	}
	if c.Nbf != nil && now.Add(skew).Before(c.Nbf.time()) {
		return nil, errors.New("token not valid yet")
	}
	if now.Add(skew).Before(c.Iat.time()) {
		return nil, errors.New("token issued in the future")
	}
	email := strings.ToLower(strings.TrimSpace(c.Email))
	if len(v.emails) > 0 && (email == "" || !slices.Contains(v.emails, email)) {
		return nil, errors.New("email not on the allowlist")
	}
	return &Claims{Email: email, Sub: c.Sub}, nil
}

// key returns the signing key kid, fetching the team's keys when the cache
// is old or does not know kid.
func (v *Verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	now := v.now()
	k, known := v.keys[kid]
	stale := v.keys == nil || now.Sub(v.fetched) > cacheFor
	if known && !stale {
		return k, nil
	}
	// Refetch: the cache is old, or kid is new to it. Not more often than
	// refetchEvery, whatever tokens arrive.
	if v.tried.IsZero() || now.Sub(v.tried) >= refetchEvery {
		v.tried = now
		// The lock is held across the fetch on purpose: concurrent
		// requests wait for one fetch instead of each making their own.
		if keys, err := v.fetch(ctx); err == nil {
			v.keys, v.fetched = keys, now
		} else if v.keys == nil {
			return nil, fmt.Errorf("cannot fetch the Access signing keys: %v", err)
		}
		k, known = v.keys[kid]
	}
	if !known {
		return nil, errors.New("token signed with an unknown key")
	}
	return k, nil
}

type jwks struct {
	Keys []struct {
		Kid string `json:"kid"`
		Kty string `json:"kty"`
		Alg string `json:"alg"`
		N   string `json:"n"`
		E   string `json:"e"`
	} `json:"keys"`
}

func (v *Verifier) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.certsURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", v.certsURL, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxCerts))
	if err != nil {
		return nil, err
	}
	var set jwks
	if err := json.Unmarshal(b, &set); err != nil {
		return nil, fmt.Errorf("%s: %v", v.certsURL, err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" || k.Kid == "" || (k.Alg != "" && k.Alg != "RS256") {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		if pub.N.BitLen() < 2048 || pub.E < 3 {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%s: no usable RS256 keys", v.certsURL)
	}
	return keys, nil
}
