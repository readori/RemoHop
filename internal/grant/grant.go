// Package grant is RemoHop's scoped, expiring access for AI agents (and for
// anybody else a host owner wants to give less than everything): a grant
// names what a credential may do on a host (which services, which file
// roots, which commands), until when, how many sessions at once, whether
// each action needs the owner's approval and whether terminal output is
// recorded.
//
// The host daemon (internal/hostd) stores grants and enforces them; this
// package holds what both sides of that and the service packages share:
// parsing and checking the rules, the grant code a client redeems, the
// Guard a service asks before it acts for a granted session, and the
// asciicast recorder. It depends only on the leaf packages perm and peer,
// so service packages can import it without importing the host daemon.
package grant

import (
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/perm"
)

// Approval modes.
const (
	ApproveNone  = "none"  // nothing is asked
	ApproveFirst = "first" // the first command or file change of each session is asked
	ApproveEach  = "each"  // every command and every file change is asked
)

// DefaultMaxSessions is how many sessions a grant allows at once unless
// its owner says otherwise.
const DefaultMaxSessions = 2

// Grant is what a host owner issued, as stored on the host.
type Grant struct {
	// Name identifies the grant in every command, refusal and audit
	// entry. See ValidName.
	Name string `json:"name"`
	// TokenID is the credential the grant is redeemed with (an entry in
	// the host's pairing-token store marked with this grant's name).
	TokenID string `json:"token_id"`
	// Allow is the allow list as given (`--allow`), e.g. "shell:run-only",
	// "files:read:/home/me/project", "screenshot".
	Allow []string `json:"allow"`
	// Commands, when not empty, are the only commands that may be run:
	// each entry is a program name, optionally followed by leading
	// arguments ("git", "go test").
	Commands []string `json:"commands,omitempty"`
	// Approve is ApproveNone, ApproveFirst or ApproveEach.
	Approve string `json:"approve,omitempty"`
	// MaxSessions bounds concurrent sessions under the grant.
	MaxSessions int `json:"max_sessions"`
	// Record keeps an asciicast recording of every command's and
	// terminal's output in the host's config directory.
	Record  bool      `json:"record,omitempty"`
	Created time.Time `json:"created"`
	Expires time.Time `json:"expires"`
	// Suspended grants (`remohop host panic`) refuse logins and streams until
	// resumed.
	Suspended bool `json:"suspended,omitempty"`
}

var nameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// ValidName checks a grant name: 1 to 64 letters, digits, '.', '_' or '-',
// starting with a letter or digit. Names appear unquoted in refusals and
// file names, so nothing else is allowed.
func ValidName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("invalid grant name %q: use 1-64 letters, digits, '.', '_' or '-', starting with a letter or digit", name)
	}
	return nil
}

// State is a grant's standing at a moment.
type State string

const (
	StateActive    State = "active"
	StateExpired   State = "expired"
	StateSuspended State = "suspended"
)

// StateAt reports the grant's state at now.
func (g *Grant) StateAt(now time.Time) State {
	switch {
	case g.Suspended:
		return StateSuspended
	case !g.Expires.IsZero() && !now.Before(g.Expires):
		return StateExpired
	}
	return StateActive
}

// Inactive returns why the grant cannot be used at now, or nil.
func (g *Grant) Inactive(now time.Time) error {
	switch g.StateAt(now) {
	case StateSuspended:
		return fmt.Errorf("grant %s is suspended (remohop host panic); its owner can resume it with `remohop host resume %s`", g.Name, g.Name)
	case StateExpired:
		return fmt.Errorf("grant %s expired at %s", g.Name, g.Expires.UTC().Format(time.RFC3339))
	}
	return nil
}

// Rules parses the grant's allow list. Every Rules of the same credential
// (name, token and roots) shares one Scope, pinned the first time the
// grant is loaded: re-reading the grants file does not re-pin its roots.
func (g *Grant) Rules() (*Rules, error) {
	r, err := Parse(g.Allow, g.Commands)
	if err != nil {
		return nil, err
	}
	key := strings.Join([]string{g.Name, g.TokenID, strings.Join(r.ReadRoots, "\x00"), strings.Join(r.WriteRoots, "\x00"),
		fmt.Sprint(r.ReadAll, r.WriteAll)}, "\x01")
	r.scope = sharedScope(key, r)
	return r, nil
}

// ShellMode says how much of the shell feature a grant allows.
type ShellMode int

const (
	ShellNone    ShellMode = iota
	ShellRunOnly           // commands and background jobs, no terminal
	ShellFull              // everything, interactive shells included
)

// Rules is a parsed allow list: what a granted session may do. The zero
// value allows nothing.
type Rules struct {
	features map[perm.Feature]bool
	services map[string]bool

	Shell ShellMode
	// Commands restricts which commands run (see CheckCommand); empty
	// means any.
	Commands []string
	// ReadAll and WriteAll allow reading and changing files anywhere the
	// host's user can.
	ReadAll, WriteAll bool
	// ReadRoots and WriteRoots are directories (absolute, cleaned) below
	// which files may be read, or read and changed.
	ReadRoots, WriteRoots []string
	// Input allows keyboard and mouse control.
	Input bool

	scope     *Scope
	scopeOnce sync.Once
}

// Items is the allow-list vocabulary, for help texts and errors.
const Items = "shell, shell:run-only, shell:cmd=PROGRAM, files, files:read, files:read:PATH, files:write:PATH, " +
	"screenshot, screen, desktop, input, clip, info, forward, vpn, serial, usb"

// Parse turns an allow list into Rules. Each item may itself be a
// comma-separated list. commands adds to the command allow-list (as
// shell:cmd= items do) and implies shell:run-only.
func Parse(allow, commands []string) (*Rules, error) {
	r := &Rules{features: map[perm.Feature]bool{}, services: map[string]bool{}}
	runOnly := func() {
		for _, s := range []string{"exec", "spawn", "jobs"} {
			r.services[s] = true
		}
		if r.Shell < ShellRunOnly {
			r.Shell = ShellRunOnly
		}
	}
	var items []string
	for _, a := range allow {
		for _, it := range strings.Split(a, ",") {
			if it = strings.TrimSpace(it); it != "" {
				items = append(items, it)
			}
		}
	}
	for _, it := range items {
		kind, rest, _ := strings.Cut(it, ":")
		switch strings.ToLower(kind) {
		case "shell":
			switch {
			case rest == "":
				r.features[perm.Shell] = true
				r.Shell = ShellFull
			case rest == "run-only":
				runOnly()
			case strings.HasPrefix(rest, "cmd="):
				c := strings.Join(strings.Fields(rest[len("cmd="):]), " ")
				if c == "" {
					return nil, fmt.Errorf("allow %q: empty command", it)
				}
				runOnly()
				r.Commands = append(r.Commands, c)
			default:
				return nil, fmt.Errorf("allow %q: shell takes no constraint, run-only or cmd=PROGRAM", it)
			}
		case "files", "file", "fs":
			mode, path, _ := strings.Cut(rest, ":")
			switch strings.ToLower(mode) {
			case "", "write", "rw":
				if path == "" {
					r.features[perm.File] = true
					r.ReadAll, r.WriteAll = true, true
					break
				}
				root, err := cleanRoot(path)
				if err != nil {
					return nil, fmt.Errorf("allow %q: %w", it, err)
				}
				r.services["fs"], r.services["fs.get"], r.services["fs.put"] = true, true, true
				r.WriteRoots = appendNew(r.WriteRoots, root)
			case "read", "ro":
				r.services["fs"], r.services["fs.get"] = true, true
				if path == "" {
					r.ReadAll = true
					break
				}
				root, err := cleanRoot(path)
				if err != nil {
					return nil, fmt.Errorf("allow %q: %w", it, err)
				}
				r.ReadRoots = appendNew(r.ReadRoots, root)
			default:
				return nil, fmt.Errorf("allow %q: files takes read, read:PATH, write or write:PATH", it)
			}
		default:
			if rest != "" {
				return nil, fmt.Errorf("allow %q: %s takes no constraint", it, kind)
			}
			switch strings.ToLower(kind) {
			case "screenshot":
				r.services["screenshot"] = true
			case "screen":
				r.features[perm.Screen] = true
			case "desktop":
				r.features[perm.Screen], r.features[perm.Input] = true, true
				r.Input = true
			case "input":
				r.features[perm.Input] = true
				r.Input = true
			case "clip", "clipboard":
				r.features[perm.Clip] = true
			case "info", "sysinfo":
				r.features[perm.Info] = true
			case "forward":
				r.features[perm.Forward] = true
			case "vpn":
				r.features[perm.VPN] = true
			case "serial":
				r.features[perm.Serial] = true
			case "usb":
				r.features[perm.USB] = true
			default:
				return nil, fmt.Errorf("unknown allow item %q (known: %s)", it, Items)
			}
		}
	}
	for _, c := range commands {
		c = strings.Join(strings.Fields(c), " ")
		if c == "" {
			continue
		}
		runOnly()
		r.Commands = appendNew(r.Commands, c)
	}
	if r.Shell == ShellFull && len(r.Commands) > 0 {
		return nil, errors.New("a command allow-list (shell:cmd=, --cmd) cannot be combined with full shell access; use it with shell:run-only")
	}
	if r.WriteAll {
		r.ReadRoots, r.WriteRoots = nil, nil
	} else if r.ReadAll {
		r.ReadRoots = nil
	}
	return r, nil
}

func appendNew(list []string, v string) []string {
	if slices.Contains(list, v) {
		return list
	}
	return append(list, v)
}

// cleanRoot makes a file root absolute and clean. "~" means the home
// directory of whoever issues the grant, which is the user the host runs as.
func cleanRoot(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, p[1:])
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("file root %q must be an absolute path", p)
	}
	return filepath.Clean(p), nil
}

// AllowsService reports whether the rules let a session open service.
// Services in perm.Always are not this function's business: the host
// decides about those itself.
func (r *Rules) AllowsService(service string) bool {
	if r.services[service] {
		if service == "shell" {
			return r.Shell == ShellFull
		}
		return true
	}
	f, ok := perm.Of(service)
	if !ok {
		return false
	}
	return r.features[f]
}

// AllowsFeature reports whether the rules allow all of feature: used for
// abilities that services check on their own (perm.Input inside the
// desktop service).
func (r *Rules) AllowsFeature(f perm.Feature) bool {
	if f == perm.Input {
		return r.Input
	}
	return r.features[f]
}

// ServiceLabel names what a refused service stands for in the allow-list
// vocabulary, so a refusal tells the reader what to ask for.
func ServiceLabel(service string) string {
	switch service {
	case "shell":
		return "shell (interactive terminal)"
	case "exec", "spawn", "jobs":
		return "shell:run-only (" + service + ")"
	case "fs", "fs.get":
		return "files:read (" + service + ")"
	case "fs.put":
		return "files:write (" + service + ")"
	case "screenshot":
		return "screenshot"
	case "desktop":
		return "screen (desktop viewing)"
	case "input":
		return "input"
	case "clip":
		return "clip"
	case "sysinfo":
		return "info"
	}
	if f, ok := perm.Of(service); ok {
		return string(f) + " (" + service + ")"
	}
	return service
}

// Summary describes the rules in the allow-list vocabulary, one capability
// per entry, for `remohop host grants` and for the agent.
func (r *Rules) Summary() []string {
	var out []string
	switch r.Shell {
	case ShellFull:
		out = append(out, "shell: commands, background jobs and interactive terminals")
	case ShellRunOnly:
		if len(r.Commands) > 0 {
			out = append(out, "shell:run-only, only these commands (program and leading arguments): "+strings.Join(r.Commands, "; "))
		} else {
			out = append(out, "shell:run-only: commands and background jobs, no interactive terminal")
		}
	}
	switch {
	case r.WriteAll:
		out = append(out, "files: read and write anywhere the host's user can")
	default:
		if r.ReadAll {
			out = append(out, "files:read anywhere the host's user can")
		} else if len(r.ReadRoots) > 0 {
			out = append(out, "files:read under "+strings.Join(r.ReadRoots, ", "))
		}
		if len(r.WriteRoots) > 0 {
			out = append(out, "files:write (and read) under "+strings.Join(r.WriteRoots, ", "))
		}
	}
	if r.features[perm.Screen] {
		out = append(out, "screen: screenshots and desktop viewing")
	} else if r.services["screenshot"] {
		out = append(out, "screenshot")
	}
	if r.Input {
		out = append(out, "input: keyboard and mouse")
	}
	for _, f := range []perm.Feature{perm.Clip, perm.Info, perm.Forward, perm.VPN, perm.Serial, perm.USB} {
		if r.features[f] {
			out = append(out, string(f))
		}
	}
	return out
}

// Denied lists the allow-list capabilities the rules do not grant, for the
// agent: what not to try.
func (r *Rules) Denied() []string {
	var out []string
	switch r.Shell {
	case ShellNone:
		out = append(out, "shell")
	case ShellRunOnly:
		out = append(out, "interactive shell")
	}
	if !r.ReadAll && !r.WriteAll && len(r.ReadRoots) == 0 && len(r.WriteRoots) == 0 {
		out = append(out, "files")
	} else if !r.WriteAll && len(r.WriteRoots) == 0 {
		out = append(out, "files:write")
	}
	if !r.features[perm.Screen] && !r.services["screenshot"] {
		out = append(out, "screenshot")
	} else if !r.features[perm.Screen] {
		out = append(out, "desktop viewing")
	}
	if !r.Input {
		out = append(out, "input")
	}
	for _, f := range []perm.Feature{perm.Clip, perm.Info, perm.Forward, perm.VPN, perm.Serial, perm.USB} {
		if !r.features[f] {
			out = append(out, string(f))
		}
	}
	return out
}

// Code is the credential string a host owner hands to whoever redeems the
// grant: "remohopg1-" + token ID + "-" + base64url(secret).
const codePrefix = "remohopg1-"

// EncodeCode formats a grant credential.
func EncodeCode(tokenID string, secret []byte) string {
	return codePrefix + tokenID + "-" + base64.RawURLEncoding.EncodeToString(secret)
}

// ParseCode reads a credential made by EncodeCode.
func ParseCode(code string) (tokenID string, secret []byte, err error) {
	code = strings.TrimSpace(code)
	rest, ok := strings.CutPrefix(code, codePrefix)
	if !ok {
		return "", nil, errors.New("not a RemoHop grant code (it should start with " + codePrefix + ")")
	}
	id, sec, ok := strings.Cut(rest, "-")
	if !ok || len(id) == 0 || len(id) > 64 {
		return "", nil, errors.New("malformed grant code")
	}
	if _, err := hex.DecodeString(id); err != nil {
		return "", nil, errors.New("malformed grant code")
	}
	secret, err = base64.RawURLEncoding.DecodeString(sec)
	if err != nil || len(secret) != 32 {
		return "", nil, errors.New("malformed grant code")
	}
	return id, secret, nil
}

// ParseApprove validates an --approve value.
func ParseApprove(v string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", ApproveNone, "no", "off":
		return ApproveNone, nil
	case ApproveFirst, "once":
		return ApproveFirst, nil
	case ApproveEach, "every", "all":
		return ApproveEach, nil
	}
	return "", fmt.Errorf("--approve %q: use each, first or none", v)
}
