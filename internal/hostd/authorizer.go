package hostd

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/grant"
)

// Authorizer lets a feature built on the host vouch for keys the owner
// never paired, with a grant: a key it admits logs in as if it had redeemed
// that grant, and the host enforces it as it enforces grants its owner
// issued (allowed services, file roots, commands, approvals, recording,
// session limit, audit). RemoHop Teams uses it for its team policy.
//
// Grant is asked at login and again at every permission check of the
// session, so a withdrawn or changed grant ends the session at its next
// stream, command or file operation, and within seconds otherwise
// (enforceGrants). It must be quick and must not block.
type Authorizer struct {
	// Name identifies the authorizer; its sessions' scope is
	// AuthorizerScope(Name).
	Name string
	// Grant returns the grant key may use now, and a display name for the
	// session. g.Name must pass grant.ValidName. Expires and Suspended are
	// honoured; TokenID, Created and the other bookkeeping fields are not
	// used.
	Grant func(h *Host, key ed25519.PublicKey) (g grant.Grant, name string, ok bool)
}

// RegisterAuthorizer adds an authorizer. Call it from init(). Keys the
// owner authorised, and scopes, are consulted first.
func RegisterAuthorizer(a Authorizer) {
	extensions.mu.Lock()
	defer extensions.mu.Unlock()
	extensions.authorizers = append(extensions.authorizers, &a)
}

const authorizerScope = "grant-by:"

// AuthorizerScope is the auth.Result.Scope of the sessions an authorizer
// admitted.
func AuthorizerScope(name string) string { return authorizerScope + name }

// authorizerName is the inverse of AuthorizerScope: the authorizer that
// admitted a session with this scope, or "" for any other scope.
func authorizerName(scope string) string {
	name, _ := strings.CutPrefix(scope, authorizerScope)
	if name == scope {
		return ""
	}
	return name
}

func authorizerFor(scope string) *Authorizer {
	name, ok := strings.CutPrefix(scope, authorizerScope)
	if !ok {
		return nil
	}
	extensions.mu.Lock()
	defer extensions.mu.Unlock()
	for _, a := range extensions.authorizers {
		if a.Name == name {
			return a
		}
	}
	return nil
}

// usableGrant reports whether an authorizer's grant can admit a login.
func usableGrant(g grant.Grant) bool {
	return grant.ValidName(g.Name) == nil && g.Inactive(time.Now()) == nil
}

// grantOf returns the grant a session (or a login about to become one)
// stands on, if it may use it now: an owner's grant by its credential, or
// an authorizer's by asking it again.
func (h *Host) grantOf(name, tokenID string, a *Authorizer, key ed25519.PublicKey) (*grantEntry, error) {
	if a == nil {
		return h.grantFor(name, tokenID)
	}
	if GrantsHeld(h.dir) {
		return nil, errHeld(a.Name)
	}
	g, _, ok := a.Grant(h, key)
	if !ok {
		return nil, fmt.Errorf("access through %s was withdrawn", a.Name)
	}
	if g.Name != name {
		return nil, fmt.Errorf("access through %s changed from %s to %s", a.Name, name, g.Name)
	}
	if err := g.Inactive(time.Now()); err != nil {
		return nil, err
	}
	return h.authorized.entry(g)
}

// rulesCache keeps authorizer grants parsed: they are asked for at every
// permission check.
type rulesCache struct {
	mu sync.Mutex
	m  map[string]*grantEntry
}

func (c *rulesCache) entry(g grant.Grant) (*grantEntry, error) {
	b, _ := json.Marshal(struct {
		Name, Approve   string
		Allow, Commands []string
		MaxSessions     int
		Record          bool
	}{g.Name, g.Approve, g.Allow, g.Commands, g.MaxSessions, g.Record})
	key := string(b)
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.m[key]; ok {
		return e, nil
	}
	if err := grant.ValidName(g.Name); err != nil {
		return nil, err
	}
	r, err := g.Rules()
	if err != nil {
		return nil, fmt.Errorf("grant %s cannot be enforced (%v); it allows nothing", g.Name, err)
	}
	if c.m == nil || len(c.m) >= 1024 {
		c.m = map[string]*grantEntry{}
	}
	e := &grantEntry{g: g, rules: r}
	c.m[key] = e
	return e, nil
}
