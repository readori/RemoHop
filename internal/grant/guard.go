package grant

import (
	"context"
	"io/fs"

	"github.com/readori/RemoHop/internal/peer"
)

// Guard is what a host service asks before it acts for a session that
// stands on a grant. The host daemon attaches one to every such session,
// and marks every session with full access as such (AttachFull); for those
// Of returns nil, and services then skip the checks entirely.
//
// A host-side session with neither (one whose setup did not finish, or a
// service reached by some path that never set it up) does not get full
// access: Of returns a guard that refuses everything.
//
// Every method that can refuse returns an error whose text is meant for
// the client: usually a *Refusal, or the owner's denial of an approval.
// Methods may block while the host's owner is asked to approve.
type Guard interface {
	// Name is the grant's name.
	Name() string
	// Command is asked before a command is started: kind is "exec" or
	// "spawn", tty whether it gets a terminal, env the extra environment
	// the client asked for.
	Command(ctx context.Context, kind, command string, tty bool, env []string) error
	// Terminal is asked before an interactive terminal is attached.
	Terminal(ctx context.Context, session string) error
	// File is asked before a file operation on host path p. op names the
	// operation for the audit log ("open", "remove", ...).
	File(ctx context.Context, op string, a Access, p string) error
	// FileScope is the grant's file scope: the pinned roots the file
	// service does its I/O through once File has allowed an operation.
	// It is the same Scope for as long as the grant's credential stays
	// the same, so a service can tell a reissued grant apart.
	FileScope() (*Scope, error)
	// Log records an action that needs no permission of its own in the
	// audit log (for example killing a background job).
	Log(action string, kv ...string)
	// Record returns a recorder for a terminal or command's output, or
	// nil if the grant does not record.
	Record(kind, title string, cols, rows int) Recorder
}

// Owned is a Guard that tells apart the identities using its grant. A grant
// an authorizer gives (RemoHop Teams' team rules) can be used by many
// client keys at once; what one of them leaves running must not be the
// others'.
type Owned interface {
	// Owner names who owns the background jobs and terminals a session
	// starts: the grant's name and the client's identity.
	Owner() string
}

// OwnerOf is the owner of what a session under g starts (see Owned), or
// the grant's name when g does not say.
func OwnerOf(g Guard) string {
	if o, ok := g.(Owned); ok {
		if s := o.Owner(); s != "" {
			return s
		}
	}
	return g.Name()
}

// Recorder receives a terminal stream as it happens. Implementations are
// safe for concurrent use and never block the stream for long; Close
// writes the end marker.
type Recorder interface {
	Output(p []byte)
	Input(p []byte)
	Resize(cols, rows int)
	Close(exit string)
}

type guardKey struct{}

// fullAccess marks a session that stands on no grant.
type fullAccess struct{}

// Attach makes g the guard of c. The host daemon calls it before the
// session serves its first stream.
func Attach(c *peer.Conn, g Guard) { c.SetValue(guardKey{}, g) }

// AttachFull records that c stands on no grant: Of(c) is nil and services
// do not restrict it. The host daemon calls it, instead of Attach, before a
// session with full access serves its first stream.
func AttachFull(c *peer.Conn) { c.SetValue(guardKey{}, fullAccess{}) }

// Of returns the guard of c, or nil if the session is not under a grant.
// For the host side of a session that was given neither a guard nor
// AttachFull it returns a guard that refuses everything: missing grant
// state never means full access. The controlling side of a session (a
// client serving callbacks from its host) is not restricted.
func Of(c *peer.Conn) Guard {
	if c == nil {
		return nil
	}
	v, _ := c.Value(guardKey{})
	switch g := v.(type) {
	case Guard:
		return g
	case fullAccess:
		return nil
	}
	if c.Role() == peer.RoleHost {
		return unset{}
	}
	return nil
}

// unset is the guard of a host session whose grant state was never set.
type unset struct{}

// unsetName is no valid grant name (ValidName), so nothing keyed by a
// grant's name (the terminals a grant started, say) matches it.
const unsetName = "(no grant state)"

// ErrNoGrantState is what the guard of a host session without grant state
// answers to everything.
var ErrNoGrantState error = noGrantState{}

type noGrantState struct{}

func (noGrantState) Error() string {
	return "refused: the host never set up this session's permissions"
}

// Is makes it match fs.ErrPermission, like a Refusal.
func (noGrantState) Is(target error) bool { return target == fs.ErrPermission }

func (unset) refuse() error { return ErrNoGrantState }

func (unset) Name() string  { return unsetName }
func (unset) Owner() string { return unsetName }
func (u unset) Command(context.Context, string, string, bool, []string) error {
	return u.refuse()
}
func (u unset) Terminal(context.Context, string) error             { return u.refuse() }
func (u unset) File(context.Context, string, Access, string) error { return u.refuse() }
func (unset) Log(string, ...string)                                {}
func (unset) Record(string, string, int, int) Recorder             { return nil }
func (u unset) FileScope() (*Scope, error)                         { return nil, u.refuse() }
