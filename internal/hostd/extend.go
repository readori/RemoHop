package hostd

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"log/slog"
	"slices"
	"sync"

	"github.com/readori/RemoHop/internal/signal"
)

// Extensions and scopes let a feature that is built on top of the host
// (the mesh network in internal/mesh, which needs internal/dial and so
// cannot be imported here) run inside the host process and admit its own
// peers with a limited login.

// Scope is a limited kind of login: a key the host's owner never authorised
// with `remohop authorize`, but that a feature vouches for, may log in and use
// exactly the listed services, nothing else. Owner approval (`remohop config
// ask`) does not apply; the feature itself is what the owner switched on.
type Scope struct {
	// Name is the scope's name, recorded in the session (auth.Result.Scope).
	Name string
	// Services the session may open.
	Services []string
	// Admit decides about a key: ok, and a display name for the session.
	// It runs during the login handshake and must be quick.
	Admit func(h *Host, key ed25519.PublicKey) (name string, ok bool)
}

var extensions struct {
	mu          sync.Mutex
	scopes      []Scope
	authorizers []*Authorizer // authorizer.go
	run         []func(ctx context.Context, h *Host)
}

// RegisterScope adds a scope. Call it from init().
func RegisterScope(s Scope) {
	extensions.mu.Lock()
	defer extensions.mu.Unlock()
	extensions.scopes = append(extensions.scopes, s)
}

// RegisterExtension adds a function that runs beside every Host for as long
// as Run runs; it must return when ctx ends. Call it from init().
func RegisterExtension(fn func(ctx context.Context, h *Host)) {
	extensions.mu.Lock()
	defer extensions.mu.Unlock()
	extensions.run = append(extensions.run, fn)
}

func (h *Host) startExtensions(ctx context.Context, wg *sync.WaitGroup) {
	extensions.mu.Lock()
	run := slices.Clone(extensions.run)
	extensions.mu.Unlock()
	for _, fn := range run {
		wg.Go(func() { fn(ctx, h) })
	}
}

// scopedKey is the auth.Server's ScopedKey hook.
func (h *Host) scopedKey(key ed25519.PublicKey) (name, scope string, ok bool) {
	extensions.mu.Lock()
	scopes := slices.Clone(extensions.scopes)
	authorizers := slices.Clone(extensions.authorizers)
	extensions.mu.Unlock()
	for _, s := range scopes {
		if s.Admit == nil {
			continue
		}
		if n, ok := s.Admit(h, key); ok {
			return n, s.Name, true
		}
	}
	for _, a := range authorizers {
		if a.Grant == nil {
			continue
		}
		g, n, ok := a.Grant(h, key)
		if !ok || !usableGrant(g) {
			continue
		}
		if n == "" {
			n = g.Name
		}
		return n, AuthorizerScope(a.Name), true
	}
	return "", "", false
}

// authorizeScope decides about a stream of a session admitted by a scope.
func (s *Session) authorizeScope(service string) error {
	extensions.mu.Lock()
	defer extensions.mu.Unlock()
	for _, sc := range extensions.scopes {
		if sc.Name == s.Auth.Scope {
			if slices.Contains(sc.Services, service) {
				return nil
			}
			break
		}
	}
	return fmt.Errorf("this login is limited to %s; %q is not part of it", s.Auth.Scope, service)
}

// Dir is the host's config directory.
func (h *Host) Dir() string { return h.dir }

// Logger is the host's logger.
func (h *Host) Logger() *slog.Logger { return h.log }

// Signal is the rendezvous server the host registers with (empty without
// one) and its options, for extensions that dial other hosts through it.
func (h *Host) Signal() (string, signal.Options) { return h.cfg.Signal, h.cfg.SignalOptions }
