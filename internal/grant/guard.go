package grant

import (
	"context"

	"github.com/readori/RemoHop/internal/peer"
)

// Guard is what a host service asks before it acts for a session that
// stands on a grant. The host daemon attaches one to every such session;
// sessions with full access have none (Of returns nil), and services then
// skip the checks entirely.
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
	// Log records an action that needs no permission of its own in the
	// audit log (for example killing a background job).
	Log(action string, kv ...string)
	// Record returns a recorder for a terminal or command's output, or
	// nil if the grant does not record.
	Record(kind, title string, cols, rows int) Recorder
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

// Attach makes g the guard of c. The host daemon calls it before the
// session serves its first stream.
func Attach(c *peer.Conn, g Guard) { c.SetValue(guardKey{}, g) }

// Of returns the guard of c, or nil if the session is not under a grant.
func Of(c *peer.Conn) Guard {
	if c == nil {
		return nil
	}
	v, _ := c.Value(guardKey{})
	g, _ := v.(Guard)
	return g
}
