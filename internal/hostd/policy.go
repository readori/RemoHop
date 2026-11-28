package hostd

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/lan"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/perm"
)

// Config keys the host reads while it runs.
const (
	SettingAsk       = "ask"
	SettingAskPaired = "ask-paired"
	SettingOTP       = "otp"
	settingFeature   = "feature."
)

// FeatureSetting is the config key of a feature switch.
func FeatureSetting(f perm.Feature) string { return settingFeature + string(f) }

// policy is what the host's owner currently allows, as stored in the
// config file. It is read again whenever the file changes, so a setting
// made with `remohop config` applies to the running host at once, including to
// sessions that are already connected.
type policy struct {
	off       map[perm.Feature]bool
	ask       bool   // logins need the owner's approval
	askPaired bool   // ... even with a pairing or an authorised key
	otp       bool   // the temporary password is single-use
	fixed     string // fixed password from the config file, normalised
	lan       bool   // answer discovery on the local network (internal/lan)
	name      string // the device name setting, cleaned; "" for the host name
}

type policyCache struct {
	mu    sync.Mutex
	path  string
	mtime time.Time
	size  int64
	have  bool
	p     policy
}

func (c *policyCache) get(dir string) policy {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.path == "" {
		c.path = filepath.Join(dir, "config.json")
	}
	var mtime time.Time
	var size int64 = -1
	if st, err := os.Stat(c.path); err == nil {
		mtime, size = st.ModTime(), st.Size()
	}
	if c.have && mtime.Equal(c.mtime) && size == c.size {
		return c.p
	}
	values, err := config.AllIn(dir)
	if err != nil {
		// An unreadable file must not open anything up: keep what was in
		// force, or the defaults.
		if c.have {
			return c.p
		}
		values = nil
	}
	p := policy{off: map[perm.Feature]bool{}}
	for _, f := range perm.Features() {
		if !config.Bool(values[FeatureSetting(f)], true) {
			p.off[f] = true
		}
	}
	p.ask = config.Bool(values[SettingAsk], false)
	p.askPaired = config.Bool(values[SettingAskPaired], false)
	p.otp = config.Bool(values[SettingOTP], false)
	p.fixed = auth.NormalizePassword(values[SettingPassword])
	p.lan = lan.Allowed(values)
	p.name = auth.CleanText(values[SettingName], auth.MaxTokenName)
	c.p, c.mtime, c.size, c.have = p, mtime, size, true
	return p
}

func (h *Host) policy() policy { return h.cfg.Overrides.apply(h.policies.get(h.dir)) }

// authorize decides whether a session may open a stream for service. It is
// the session layer's Authorize hook.
func (s *Session) authorize(service string) error {
	if s.Auth.Scope != "" && s.authz == nil {
		// An authorizer's session is limited by its grant (below), not by
		// a scope's service list.
		if err := s.authorizeScope(service); err != nil {
			return err
		}
	}
	f, ok := perm.Of(service)
	if !ok {
		return fmt.Errorf("the host does not offer %q to remote clients (the service is not assigned to a permission feature)", service)
	}
	if f == perm.Always {
		if s.grant != "" {
			return s.authorizeGrant(service, f)
		}
		return nil
	}
	if s.host.policy().off[f] {
		return fmt.Errorf("the host does not allow %s (feature %q is switched off; its owner can allow it with `remohop config feature %s on`)", service, f, f)
	}
	if s.limited(f) {
		return fmt.Errorf("the host's owner did not allow this session %s (feature %q); ask for it with the access request", service, f)
	}
	if s.grant != "" {
		if err := s.authorizeGrant(service, f); err != nil {
			return err
		}
	}
	s.host.mu.Lock()
	first := !s.used[f]
	s.used[f] = true
	s.host.mu.Unlock()
	if first {
		s.host.audit.add("feature", s.fields("feature", string(f), "service", service)...)
	}
	return nil
}

// Allowed reports whether the host's owner currently allows feature for the
// session behind c. Services that offer more than their own feature covers
// ask it for the rest: the desktop service is in "screen" and calls
// Allowed(c, perm.Input) before it injects keyboard or mouse events, so
// that a host with input off still shows its screen, and WatchFeature to
// learn when the answer changes while its stream is open. A connection that
// is not a session of a running Host is restricted as grant.Of says: not at
// all if it was marked as having full access (grant.AttachFull, as test
// sessions are), entirely if nothing was set up for it.
func Allowed(c *peer.Conn, feature perm.Feature) bool {
	s := SessionOf(c)
	if s == nil {
		return grant.Of(c) == nil
	}
	return s.allows(s.host.policy(), feature)
}
