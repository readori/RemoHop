// Package mcp is `remohop mcp`: a Model Context Protocol server on stdio that
// exposes RemoHop's remote shell, port forwarding, clipboard, screenshot,
// system information and file services as tools, so an AI agent can operate
// remote machines by their 9-digit ID.
//
// The server is a thin layer over the Go client APIs of the service packages.
// It keeps one P2P session per target in a pool (see pool), never prompts for
// credentials (it has no terminal: the protocol owns stdin and stdout) and
// never writes anything but protocol messages to stdout.
package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"runtime/debug"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/peer"
)

// Defaults for Options.
const (
	DefaultIdleTimeout = 10 * time.Minute
	defaultDialTimeout = 60 * time.Second
)

const instructions = `RemoHop gives you shell, file, clipboard, screenshot and port-forward access to remote machines identified by a 9-digit ID (or a saved alias, or host:port).
Every tool takes a "target" argument; it may be omitted only when the server was started with a default target. The machine must be running "remohop host" and the person must have paired this computer once ("remohop <ID> pair"); if a tool reports an authentication failure, ask them to run the command it names — you cannot supply passwords.
Access may be limited by a grant the machine's owner issued (which tools work, which files and commands, until when, approval of each action). The host enforces it; call remohop_limits to see what you may do, and treat "not allowed by grant" errors as final rather than looking for workarounds.
Use remohop_exec for short commands (it waits, with a timeout); use remohop_spawn plus remohop_job_logs / remohop_job_wait for anything long-running. Filter output on the remote side (grep, tail, head) instead of pulling large logs into context. Use remohop_screenshot to look at the remote screen, and remohop_forward_start to reach a service on the remote machine from localhost.`

// Options configures a Server.
type Options struct {
	// Dial connects to a target. Required.
	Dial DialFunc
	// DefaultTarget is used when a tool call omits "target".
	DefaultTarget string
	// IdleTimeout closes sessions unused for this long; 0 means
	// DefaultIdleTimeout, negative disables idle closing.
	IdleTimeout time.Duration
	// DialTimeout bounds one dial; 0 means 60 seconds.
	DialTimeout time.Duration
	// Version is reported to clients.
	Version string
	// Logger receives diagnostics; it must never write to stdout. Nil
	// discards them.
	Logger *slog.Logger
	// ReadOnly offers only the tools that change nothing (no commands,
	// writes, input, clipboard changes or forwards). It is a convenience on
	// this side; what a host allows is enforced by the host (grants).
	ReadOnly bool
	// WriteDirs are the directories on this computer that tools may
	// write to (downloads, saved screenshots and clipboard contents); a
	// relative path lands in the first. Nil means DefaultDownloadDir.
	// What a tool writes comes from a remote machine and the model may
	// have been misled, so it must not reach ~/.ssh, shell startup files
	// and the like.
	WriteDirs []string
}

// Server is the MCP server. Create it with New, serve it with Run, and call
// Close to release the sessions and forwards if Run was not used.
type Server struct {
	opts  Options
	log   *slog.Logger
	pool  *pool
	fwd   *forwards
	views views
	mcp   *sdk.Server
}

// New builds a server with every RemoHop tool registered.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	switch {
	case opts.IdleTimeout == 0:
		opts.IdleTimeout = DefaultIdleTimeout
	case opts.IdleTimeout < 0:
		opts.IdleTimeout = 0
	}
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = defaultDialTimeout
	}
	if opts.Version == "" {
		opts.Version = cli.Version
	}
	s := &Server{opts: opts, log: opts.Logger}
	s.pool = newPool(opts.Dial, opts.IdleTimeout, opts.DialTimeout, s.log)
	s.fwd = newForwards()
	instr := instructions
	if dirs, err := s.writeDirs(); err == nil {
		instr += "\nFiles you download or save on this computer (remohop_download, save_path) must go in " + strings.Join(dirs, " or ") +
			"; a relative path or a bare file name lands in " + dirs[0] + "."
	}
	if opts.ReadOnly {
		instr += "\nThis server runs in read-only mode: only tools that change nothing are available."
	}
	s.mcp = sdk.NewServer(&sdk.Implementation{Name: "remohop", Title: "RemoHop", Version: opts.Version}, &sdk.ServerOptions{
		Instructions: instr,
		Logger:       s.log,
	})
	s.registerShell()
	s.registerForward()
	s.registerClipboard()
	s.registerScreenshot()
	s.registerSystem()
	s.registerFiles()
	s.registerPeers()
	s.registerInput()
	s.registerExtras()
	s.registerLimits()
	return s
}

// Protocol returns the underlying SDK server, for connecting it to a custom
// transport (tests use an in-memory one).
func (s *Server) Protocol() *sdk.Server { return s.mcp }

// Run serves MCP over t until the client disconnects or ctx is cancelled,
// then closes every session and forward.
func (s *Server) Run(ctx context.Context, t sdk.Transport) error {
	defer s.Close()
	return s.mcp.Run(ctx, t)
}

// Close stops the forwards and closes every pooled session. It is safe to
// call more than once.
func (s *Server) Close() {
	s.fwd.closeAll()
	s.pool.close()
}

// resolveTarget picks the target of a call.
func (s *Server) resolveTarget(arg string) (string, error) {
	t := strings.TrimSpace(arg)
	if t == "" {
		t = s.opts.DefaultTarget
	}
	if t == "" {
		return "", errors.New(`no target: pass the "target" argument (a 9-digit RemoHop ID, a saved alias, or host:port)`)
	}
	return cli.NormalizeTarget(t), nil
}

// withConn runs fn on the pooled session of target. A session that already
// died is replaced before fn runs. If it dies during fn and retry is set (the
// call is safe to repeat), fn is run once more on a fresh session; otherwise
// the error says the session was lost so the model knows the call may or may
// not have taken effect.
func (s *Server) withConn(ctx context.Context, targetArg string, retry bool, fn func(context.Context, *peer.Conn) error) error {
	target, err := s.resolveTarget(targetArg)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		conn, release, err := s.pool.acquire(ctx, target)
		if err != nil {
			return dialError(target, err)
		}
		err = fn(ctx, conn)
		release()
		if err == nil || ctx.Err() != nil || !sessionLost(conn, err) {
			return err
		}
		s.pool.invalidate(target, conn)
		if retry && attempt == 0 {
			s.log.Debug("session lost, retrying on a new one", "target", target)
			continue
		}
		return fmt.Errorf("the session to %s was lost during the call (%w); the next call reconnects, but this one may or may not have taken effect", target, err)
	}
}

// lostGrace is how long a transport-looking failure waits for the session
// to be reported closed before it is taken for an ordinary tool failure.
const lostGrace = 250 * time.Millisecond

// sessionLost reports whether err, returned by a call on conn, means the
// session itself is gone (so the call can be repeated on a new one) rather
// than that the host answered with a failure.
//
// A dying QUIC connection surfaces in two places, the stream operation's
// error and the connection's context, in no fixed order: a call can fail
// with "Application error 0x0 (remote): closed" a moment before conn.Done()
// is closed. So the error is classified by what it is, and when it merely
// looks like a transport failure the session gets a moment to be declared
// dead.
func sessionLost(conn *peer.Conn, err error) bool {
	if dead(conn) {
		return true
	}
	var remote *peer.RemoteError
	if errors.As(err, &remote) {
		return false // the host answered: a refusal, not a lost session
	}
	var (
		appErr   *quic.ApplicationError
		transErr *quic.TransportError
		idleErr  *quic.IdleTimeoutError
		resetErr *quic.StatelessResetError
		hsErr    *quic.HandshakeTimeoutError
		verErr   *quic.VersionNegotiationError
	)
	if errors.As(err, &appErr) || errors.As(err, &transErr) || errors.As(err, &idleErr) || errors.As(err, &resetErr) ||
		errors.As(err, &hsErr) || errors.As(err, &verErr) || errors.Is(err, net.ErrClosed) {
		return true
	}
	// Services do not always wrap the transport error, and a stream cut off
	// by a closing connection can also read as a plain EOF or a reset.
	msg := err.Error()
	var streamErr *quic.StreamError
	transportish := errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &streamErr) ||
		strings.Contains(msg, "Application error 0x") || strings.Contains(msg, "no recent network activity") ||
		strings.Contains(msg, "use of closed network connection") || strings.Contains(msg, "EOF") || strings.Contains(msg, "stream reset")
	if !transportish {
		return false
	}
	select {
	case <-conn.Done():
		return true
	case <-time.After(lostGrace):
		return false
	}
}

// dialError explains a failed connection, including the one thing the model
// cannot do itself: set up credentials.
func dialError(target string, err error) error {
	if errors.Is(err, errPoolClosed) || errors.Is(err, context.Canceled) {
		return err
	}
	switch {
	case errors.Is(err, dial.ErrNoCredential):
		return fmt.Errorf("cannot connect to %s: no saved pairing and no password available (%w).\n"+
			"The person must run `remohop %s pair` once in a terminal and enter the host's password; "+
			"then retry. This server cannot prompt for a password and does not accept one as an argument.", target, err, target)
	case errors.Is(err, dial.ErrHostKeyChanged):
		return fmt.Errorf("cannot connect to %s: %w.\n"+
			"The machine presented a different identity than the one saved earlier, which can mean a reinstall or an interception. "+
			"Do not work around this; tell the person so they can check and run `remohop peers forget %s` if it is expected.", target, err, target)
	}
	if auth.CodeOf(err) == auth.CodeDenied {
		return fmt.Errorf("cannot connect to %s: %w.\n"+
			"The host refused the login after checking the credential: its owner denied or did not answer an approval request, "+
			"or this computer's grant has expired, been revoked or suspended, or has reached its session limit. Tell the person; do not retry in a loop.", target, err)
	}
	return fmt.Errorf("cannot connect to %s: %w\n"+
		"If this is an authentication failure (wrong or missing pairing), the person has to run `remohop %s pair` once in a terminal; "+
		"this server cannot prompt for a password. Otherwise check that `remohop host` is running on the target and that the ID or address is right.",
		target, err, target)
}

// reply is what a tool handler returns: structured output plus the content
// blocks shown to the model (text, and for some tools an image).
type reply[Out any] struct {
	Out     Out
	Content []sdk.Content
}

func textReply[Out any](out Out, text string) *reply[Out] {
	return &reply[Out]{Out: out, Content: []sdk.Content{&sdk.TextContent{Text: text}}}
}

// addTool registers a typed tool. The SDK derives the input and output JSON
// Schemas from In and Out, validates arguments before fn runs, and turns a
// returned error into an isError result with the error text.
func addTool[In, Out any](s *Server, name, title, description string, ann *sdk.ToolAnnotations, fn func(context.Context, In) (*reply[Out], error)) {
	if s.opts.ReadOnly && !ann.ReadOnlyHint {
		return
	}
	ann.Title = title
	sdk.AddTool(s.mcp, &sdk.Tool{Name: name, Title: title, Description: description, Annotations: ann},
		func(ctx context.Context, _ *sdk.CallToolRequest, in In) (res *sdk.CallToolResult, out Out, err error) {
			defer func() {
				if r := recover(); r != nil {
					s.log.Error("tool panicked", "tool", name, "panic", r, "stack", string(debug.Stack()))
					err = fmt.Errorf("internal error in %s: %v", name, r)
				}
			}()
			r, err := fn(ctx, in)
			if err != nil {
				return nil, out, errors.New(grantHint(clean(err.Error())))
			}
			return &sdk.CallToolResult{Content: r.Content}, r.Out, nil
		})
}

// Annotation constructors. All tools act on a remote machine, which is an
// open world; the few that only touch local state say so.
func readOnly() *sdk.ToolAnnotations {
	return &sdk.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(true)}
}

func localReadOnly() *sdk.ToolAnnotations {
	return &sdk.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}
}

// additive changes state without destroying anything that existed.
func additive() *sdk.ToolAnnotations {
	return &sdk.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(true)}
}

// destructive may overwrite or destroy remote state; idempotent says that
// repeating the identical call has no further effect.
func destructive(idempotent bool) *sdk.ToolAnnotations {
	return &sdk.ToolAnnotations{DestructiveHint: ptr(true), IdempotentHint: idempotent, OpenWorldHint: ptr(true)}
}

func ptr[T any](v T) *T { return &v }

// clamp returns v, or def when v <= 0, limited to max.
func clamp(v, def, max int) int {
	if v <= 0 {
		v = def
	}
	return min(v, max)
}

// idempotentLocal is for tools that change only this server's own state
// without destroying data, where repeating the call is harmless.
func idempotentLocal() *sdk.ToolAnnotations {
	return &sdk.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(false)}
}
