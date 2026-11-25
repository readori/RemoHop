package hostd

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/perm"
)

// GrantsFile holds the host's grants (internal/grant) in the config
// directory, mode 0600. Their credentials are entries of the pairing-token
// store marked with the grant's name.
const GrantsFile = "grants.json"

// RecordingsDir is where recordings of granted sessions are kept, one
// subdirectory per grant, inside the config directory.
const RecordingsDir = "recordings"

// approvalMemory is how long an approved file change covers further
// changes to the same path in the same session, so one upload (open,
// write, set times, rename) is asked about once, not once per step.
const approvalMemory = time.Minute

// GrantStore is the host's grants file. Like the token store, every
// operation goes to the file, under a cross-process lock for changes, so
// `remohop host grant`, `revoke` and `panic` run from another process apply to
// a running host at once.
type GrantStore struct {
	path string
}

// OpenGrantStore returns the grant store in dir.
func OpenGrantStore(dir string) *GrantStore {
	return &GrantStore{path: filepath.Join(dir, GrantsFile)}
}

// List returns every grant, oldest first.
func (s *GrantStore) List() ([]grant.Grant, error) {
	b, err := config.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var gs []grant.Grant
	if err := json.Unmarshal(b, &gs); err != nil {
		return nil, fmt.Errorf("%s: %w", s.path, err)
	}
	return gs, nil
}

// Get finds a grant by name.
func (s *GrantStore) Get(name string) (grant.Grant, bool, error) {
	gs, err := s.List()
	if err != nil {
		return grant.Grant{}, false, err
	}
	for _, g := range gs {
		if g.Name == name {
			return g, true, nil
		}
	}
	return grant.Grant{}, false, nil
}

func (s *GrantStore) update(fn func([]grant.Grant) ([]grant.Grant, error)) error {
	unlock, err := config.Lock(s.path)
	if err != nil {
		return err
	}
	defer unlock()
	gs, err := s.List()
	if err != nil {
		return err
	}
	if gs, err = fn(gs); err != nil {
		return err
	}
	b, err := json.MarshalIndent(gs, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(s.path, append(b, '\n'), 0o600)
}

// GrantRequest describes a grant to issue.
type GrantRequest struct {
	Name     string
	For      time.Duration // how long it is valid
	Allow    []string      // allow-list items (see grant.Parse)
	Commands []string      // command allow-list
	Approve  string        // grant.ApproveNone, ApproveFirst or ApproveEach
	// MaxSessions bounds concurrent sessions; 0 means
	// grant.DefaultMaxSessions.
	MaxSessions int
	Record      bool
	// ClientKey, if set, binds the credential to that client identity;
	// otherwise the first client to log in with it is bound.
	ClientKey ed25519.PublicKey
}

// IssueGrant creates (or replaces) a grant in the host config directory
// dir and returns it with the credential its holder redeems
// (`remohop peers redeem TARGET CODE`). Reissuing a name replaces the grant
// and its credential; sessions on the old credential are closed.
func IssueGrant(dir string, req GrantRequest) (grant.Grant, string, error) {
	if err := grant.ValidName(req.Name); err != nil {
		return grant.Grant{}, "", err
	}
	if req.For <= 0 {
		return grant.Grant{}, "", errors.New("a grant needs a positive duration (--for)")
	}
	rules, err := grant.Parse(req.Allow, req.Commands)
	if err != nil {
		return grant.Grant{}, "", err
	}
	if len(rules.Summary()) == 0 {
		return grant.Grant{}, "", errors.New("the grant allows nothing; name what it may do with --allow (" + grant.Items + ")")
	}
	approve, err := grant.ParseApprove(req.Approve)
	if err != nil {
		return grant.Grant{}, "", err
	}
	if req.MaxSessions < 0 {
		return grant.Grant{}, "", errors.New("--sessions must be at least 1")
	}
	if req.MaxSessions == 0 {
		req.MaxSessions = grant.DefaultMaxSessions
	}
	// Store the roots as resolved, so that `remohop host grants` shows what is
	// enforced (a "~" becomes the host user's home).
	allow := normalizeAllow(req.Allow)
	tokens := auth.OpenTokenStore(filepath.Join(dir, auth.TokensFile))
	tok, err := tokens.MintGrant(req.Name, req.ClientKey)
	if err != nil {
		return grant.Grant{}, "", err
	}
	now := time.Now().UTC().Truncate(time.Second)
	g := grant.Grant{Name: req.Name, TokenID: tok.ID, Allow: allow, Commands: req.Commands, Approve: approve,
		MaxSessions: req.MaxSessions, Record: req.Record, Created: now, Expires: now.Add(req.For)}
	err = OpenGrantStore(dir).update(func(gs []grant.Grant) ([]grant.Grant, error) {
		kept := gs[:0]
		for _, x := range gs {
			if x.Name != g.Name {
				kept = append(kept, x)
			}
		}
		return append(kept, g), nil
	})
	if err != nil {
		tokens.RevokeGrant(req.Name)
		return grant.Grant{}, "", err
	}
	return g, grant.EncodeCode(tok.ID, tok.Secret), nil
}

// normalizeAllow splits comma lists and expands "~" in file roots.
func normalizeAllow(allow []string) []string {
	var out []string
	for _, a := range allow {
		for _, it := range strings.Split(a, ",") {
			it = strings.TrimSpace(it)
			if it == "" {
				continue
			}
			kind, rest, _ := strings.Cut(it, ":")
			mode, path, hasPath := strings.Cut(rest, ":")
			if (kind == "files" || kind == "file" || kind == "fs") && hasPath && (path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`)) {
				if home, err := os.UserHomeDir(); err == nil {
					it = kind + ":" + mode + ":" + filepath.Join(home, path[1:])
				}
			}
			out = append(out, it)
		}
	}
	return out
}

// RevokeGrant deletes a grant and its credential. It reports whether there
// was such a grant (or credential).
func RevokeGrant(dir, name string) (bool, error) {
	found := false
	err := OpenGrantStore(dir).update(func(gs []grant.Grant) ([]grant.Grant, error) {
		kept := gs[:0]
		for _, g := range gs {
			if g.Name == name {
				found = true
				continue
			}
			kept = append(kept, g)
		}
		return kept, nil
	})
	if err != nil {
		return false, err
	}
	n, err := auth.OpenTokenStore(filepath.Join(dir, auth.TokensFile)).RevokeGrant(name)
	return found || n > 0, err
}

// SuspendGrants suspends (or with suspend false, resumes) the named grants,
// or all of them when names is empty, and returns the names changed.
func SuspendGrants(dir string, suspend bool, names ...string) ([]string, error) {
	var changed []string
	err := OpenGrantStore(dir).update(func(gs []grant.Grant) ([]grant.Grant, error) {
		changed = changed[:0]
		for i := range gs {
			match := len(names) == 0
			for _, n := range names {
				match = match || gs[i].Name == n
			}
			if match && gs[i].Suspended != suspend {
				gs[i].Suspended = suspend
				changed = append(changed, gs[i].Name)
			}
		}
		return gs, nil
	})
	return changed, err
}

// grantEntry is a grant as loaded, with its parsed rules.
type grantEntry struct {
	g     grant.Grant
	rules *grant.Rules
	err   error // the allow list no longer parses: the grant allows nothing
}

// grantCache keeps the grants file parsed, re-reading it when it changes,
// like policyCache does for the config file.
type grantCache struct {
	mu    sync.Mutex
	mtime time.Time
	size  int64
	have  bool
	m     map[string]*grantEntry
}

func (c *grantCache) get(dir, name string) (*grantEntry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	path := filepath.Join(dir, GrantsFile)
	var mtime time.Time
	var size int64 = -1
	if st, err := os.Stat(path); err == nil {
		mtime, size = st.ModTime(), st.Size()
	}
	if !c.have || !mtime.Equal(c.mtime) || size != c.size {
		gs, err := OpenGrantStore(dir).List()
		if err == nil || !c.have {
			// An unreadable file never opens anything up: keep what was
			// loaded, and with nothing loaded no grant exists.
			m := map[string]*grantEntry{}
			for _, g := range gs {
				r, perr := g.Rules()
				m[g.Name] = &grantEntry{g: g, rules: r, err: perr}
			}
			c.m, c.mtime, c.size, c.have = m, mtime, size, true
		}
	}
	e, ok := c.m[name]
	return e, ok
}

// invalidate makes the next get re-read the file, for changes that a
// coarse file-system clock could hide from the modification-time check.
func (c *grantCache) invalidate() {
	c.mu.Lock()
	c.mtime = time.Time{}
	c.size = -2
	c.mu.Unlock()
}

// activeGrant returns the session's grant if it may be used now.
func (h *Host) activeGrant(name string) (*grantEntry, error) {
	e, ok := h.grants.get(h.dir, name)
	if !ok {
		return nil, fmt.Errorf("grant %s was revoked", name)
	}
	if err := e.g.Inactive(time.Now()); err != nil {
		return nil, err
	}
	if e.err != nil {
		return nil, fmt.Errorf("grant %s cannot be enforced (%v); it allows nothing", name, e.err)
	}
	return e, nil
}

// grantFor returns the grant called name if a holder of the credential
// tokenID may use it now: the grant is active and is still the one that
// credential was issued for. Reissuing a grant under the same name gives
// it a new credential, so the new grant's rules never apply to sessions on
// the old one.
func (h *Host) grantFor(name, tokenID string) (*grantEntry, error) {
	e, err := h.activeGrant(name)
	if err == nil && e.g.TokenID != tokenID {
		// The cache may be behind a reissue that a coarse file-system
		// clock hid from it (a new credential that looks unknown), so
		// look once more before deciding.
		h.grants.invalidate()
		if e, err = h.activeGrant(name); err == nil && e.g.TokenID != tokenID {
			err = fmt.Errorf("grant %s was reissued; the old credential no longer works", name)
		}
	}
	if err != nil {
		return nil, err
	}
	return e, nil
}

// grantLogin decides whether a client presenting a grant's credential may
// log in now: the grant exists, is active, is the one the credential was
// issued for, and has a session to spare. admit checks all of it again
// before the session is let in.
func (h *Host) grantLogin(res *auth.Result) error {
	e, err := h.grantFor(res.Grant, res.TokenID)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.grantFullLocked(res.Grant, e.g.MaxSessions)
}

// grantFullLocked reports a grant whose live sessions are at its limit.
func (h *Host) grantFullLocked(name string, max int) error {
	n := 0
	for s := range h.sessions {
		if s.grant == name {
			n++
		}
	}
	if max <= 0 {
		max = grant.DefaultMaxSessions
	}
	if n >= max {
		return fmt.Errorf("grant %s allows at most %d concurrent session(s)", name, max)
	}
	return nil
}

// enforceGrants closes granted sessions whose grant has expired, been
// revoked, reissued or suspended, and returns how many it closed.
func (h *Host) enforceGrants() int {
	h.grants.invalidate()
	n := 0
	for _, s := range h.Sessions() {
		if s.grant == "" {
			continue
		}
		if _, err := h.grantFor(s.grant, s.currentTokenID()); err != nil {
			h.log.Info("closing granted session", "grant", s.grant, "from", s.Remote, "reason", err)
			h.audit.add("grant-end", s.fields("reason", err.Error())...)
			s.conn.CloseWithReason(err.Error())
			n++
		}
	}
	return n
}

// endGrantSessions closes every granted session (all sessions with all),
// for `remohop host panic`.
func (h *Host) endGrantSessions(all bool, reason string) int {
	n := 0
	for _, s := range h.Sessions() {
		if s.grant == "" && !all {
			continue
		}
		h.audit.add("panic", s.fields("reason", reason)...)
		s.conn.CloseWithReason(reason)
		n++
	}
	return n
}

// Grant is the name of the grant the session stands on, or "" for a
// session with full access.
func (s *Session) Grant() string { return s.grant }

// activeGrant returns the session's grant if the session may still use
// it: active, and still the grant its credential was issued for (grantFor).
// Every permission check of a granted session goes through it. Otherwise
// the session is over: it is closed, and the error says why to whoever
// asked.
func (s *Session) activeGrant() (*grantEntry, error) {
	e, err := s.host.grantFor(s.grant, s.currentTokenID())
	if err != nil {
		go s.conn.CloseWithReason(err.Error())
		return nil, err
	}
	return e, nil
}

// grantAllowsFeature is Allowed's answer for granted sessions.
func (s *Session) grantAllowsFeature(f perm.Feature) bool {
	e, err := s.activeGrant()
	return err == nil && e.rules.AllowsFeature(f)
}

// authorizeGrant is authorize's check for a granted session.
func (s *Session) authorizeGrant(service string, f perm.Feature) error {
	e, err := s.activeGrant()
	if err != nil {
		// The session is over (activeGrant ends it); say why on this
		// stream.
		return err
	}
	if f == perm.Always {
		return nil
	}
	if !e.rules.AllowsService(service) {
		s.host.audit.addRefused(grantKey(s.grant, s.Auth.ClientKey), "grant-refused", s.fields("service", service)...)
		return &grant.Refusal{Grant: s.grant, What: grant.ServiceLabel(service)}
	}
	switch service {
	case "exec", "spawn", "fs", "fs.get", "fs.put", "shell":
		// Logged in detail by the guard as each command or file
		// operation is asked for.
	default:
		s.host.audit.add("grant-action", s.fields("service", service)...)
	}
	return nil
}

// sessionGuard is the grant.Guard of a granted session.
type sessionGuard struct{ s *Session }

var _ grant.Guard = sessionGuard{}

func (g sessionGuard) Name() string { return g.s.grant }

func (g sessionGuard) refuse(kind string, err error, kv ...string) error {
	g.s.host.audit.addRefused(grantKey(g.s.grant, g.s.Auth.ClientKey), "grant-refused", g.s.fields(append([]string{"kind", kind, "reason", err.Error()}, kv...)...)...)
	return err
}

func (g sessionGuard) Command(ctx context.Context, kind, command string, tty bool, env []string) error {
	s := g.s
	e, err := s.activeGrant()
	if err != nil {
		return g.refuse(kind, err, "command", command)
	}
	r := e.rules
	switch {
	case r.Shell == grant.ShellNone:
		return g.refuse(kind, &grant.Refusal{Grant: s.grant, What: "running commands"}, "command", command)
	case tty && r.Shell != grant.ShellFull:
		return g.refuse(kind, &grant.Refusal{Grant: s.grant, What: "a terminal (tty) for a command", Why: "it allows shell:run-only"}, "command", command)
	case len(r.Commands) > 0 && len(env) > 0:
		return g.refuse(kind, &grant.Refusal{Grant: s.grant, What: "setting environment variables", Why: "it has a command allow-list"}, "command", command)
	}
	if err := grant.CheckCommand(command, r.Commands); err != nil {
		return g.refuse(kind, &grant.Refusal{Grant: s.grant, What: "command " + strconv.Quote(command), Why: err.Error()}, "command", command)
	}
	if err := s.approveAction(ctx, &e.g, kind+": "+command, ""); err != nil {
		return g.refuse(kind, err, "command", command)
	}
	s.host.audit.add("grant-action", s.fields("kind", kind, "command", command)...)
	return nil
}

func (g sessionGuard) Terminal(ctx context.Context, session string) error {
	s := g.s
	e, err := s.activeGrant()
	if err != nil {
		return g.refuse("terminal", err, "session", session)
	}
	if e.rules.Shell != grant.ShellFull {
		return g.refuse("terminal", &grant.Refusal{Grant: s.grant, What: "an interactive terminal", Why: "it allows shell:run-only"}, "session", session)
	}
	if err := s.approveAction(ctx, &e.g, "interactive shell "+strconv.Quote(session), ""); err != nil {
		return g.refuse("terminal", err, "session", session)
	}
	s.host.audit.add("grant-action", s.fields("kind", "terminal", "session", session)...)
	return nil
}

func (g sessionGuard) File(ctx context.Context, op string, a grant.Access, p string) error {
	s := g.s
	e, err := s.activeGrant()
	if err != nil {
		return g.refuse("file", err, "op", op, "path", p)
	}
	if err := e.rules.CheckPath(p, a); err != nil {
		return g.refuse("file", &grant.Refusal{Grant: s.grant, What: a.String(), Why: err.Error()}, "op", op, "path", p)
	}
	if a == grant.Write {
		if err := s.approveAction(ctx, &e.g, "change file "+p+" ("+op+")", p); err != nil {
			return g.refuse("file", err, "op", op, "path", p)
		}
	}
	if a != grant.Meta {
		s.host.audit.add("grant-action", s.fields("kind", "file", "op", op, "path", p)...)
	}
	return nil
}

func (g sessionGuard) FileScope() (*grant.Scope, error) {
	e, err := g.s.host.activeGrant(g.s.grant)
	if err != nil {
		return nil, err
	}
	return e.rules.Scope(), nil
}

func (g sessionGuard) Log(action string, kv ...string) {
	g.s.host.audit.add("grant-action", g.s.fields(append([]string{"kind", action}, kv...)...)...)
}

func (g sessionGuard) Record(kind, title string, cols, rows int) grant.Recorder {
	s := g.s
	e, err := s.activeGrant()
	if err != nil || !e.g.Record {
		return nil
	}
	s.host.mu.Lock()
	s.host.nextRecording++
	n := s.host.nextRecording
	s.host.mu.Unlock()
	name := fmt.Sprintf("%s-%s-%d.cast", time.Now().UTC().Format("20060102T150405Z"), kind, n)
	path := filepath.Join(s.host.dir, RecordingsDir, s.grant, name)
	c, err := grant.NewCast(path, cols, rows, s.grant+": "+title)
	if err != nil {
		s.host.log.Warn("could not start a recording", "grant", s.grant, "err", err)
		return nil
	}
	s.host.audit.add("grant-recording", s.fields("kind", kind, "title", title, "file", filepath.Join(RecordingsDir, s.grant, name))...)
	return c
}

// actionApprovals is a session's memory of approved actions.
type actionApprovals struct {
	mu    sync.Mutex
	first bool                 // ApproveFirst: the session has had its one approval
	paths map[string]time.Time // ApproveEach: recently approved file changes
}

// approveAction asks the host's owner about an action if the grant wants
// that. key, if not empty, lets one approval cover the same key for
// approvalMemory (file changes to one path).
func (s *Session) approveAction(ctx context.Context, g *grant.Grant, action, key string) error {
	switch g.Approve {
	case "", grant.ApproveNone:
		return nil
	}
	a := &s.approved
	a.mu.Lock()
	skip := g.Approve == grant.ApproveFirst && a.first
	if t, ok := a.paths[key]; key != "" && ok && time.Since(t) < approvalMemory {
		skip = true
	}
	a.mu.Unlock()
	if skip {
		return nil
	}
	if err := s.host.askAction(ctx, s, action); err != nil {
		return err
	}
	a.mu.Lock()
	a.first = true
	if key != "" {
		if a.paths == nil {
			a.paths = map[string]time.Time{}
		}
		for k, t := range a.paths {
			if time.Since(t) >= approvalMemory {
				delete(a.paths, k)
			}
		}
		a.paths[key] = time.Now()
	}
	a.mu.Unlock()
	return nil
}
