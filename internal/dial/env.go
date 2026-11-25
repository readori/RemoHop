package dial

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/signal"
)

func init() {
	// `remohop lab` alone: cli asks whether "lab" is a saved host.
	cli.KnownTarget = func(name string) bool {
		st, err := OpenStore("")
		if err != nil {
			return false
		}
		_, ok, err := st.ByAlias(name)
		return err == nil && ok
	}
}

// rendezvousFirst reports a missing rendezvous server for a target that
// needs one (a 9-digit ID, directly or through its alias) before anything
// else happens: before the password prompt, and instead of "no credential"
// when there is no terminal to prompt on.
func rendezvousFirst(target string, opts Options) error {
	t := cli.NormalizeTarget(target)
	if !cli.IsID(t) {
		st, err := OpenStore(opts.Dir)
		if err != nil {
			return nil
		}
		p, ok, err := st.ByAlias(t)
		if err != nil || !ok || !cli.IsID(p.Target) {
			return nil
		}
		t = p.Target
	}
	if _, _, err := signal.Resolve(opts.Signal); errors.Is(err, signal.ErrNotConfigured) {
		return &explained{msg: fmt.Sprintf("%s is a RemoHop ID, and IDs are looked up on a rendezvous server, a meeting point "+
			"that introduces this machine to its hosts; none is set up here. Set yours once with "+
			"`remohop config set signal HOST[:PORT]` (or pass --signal HOST[:PORT]), or connect by the host's address "+
			"instead: `remohop HOST:PORT <command>`", signal.FormatID(t)), err: err}
	}
	return nil
}

// OptionsFromEnv builds the Options a CLI command dials with: the global
// --signal and --password flags (or REMOHOP_SIGNAL / REMOHOP_PASSWORD), and an
// interactive password prompt only when stdin is a terminal.
func OptionsFromEnv(env *cli.Env) Options {
	opts := Options{Signal: env.Globals.Signal, Password: env.Globals.Password}
	if os.Getenv("REMOHOP_QUIET") == "" {
		opts.Notify = func(msg string) { env.Errorf("%s", msg) }
	}
	if f, ok := env.Stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		opts.Prompt = func(ctx context.Context, req PromptRequest) (string, error) {
			return promptPassword(ctx, env, f, req)
		}
	}
	return opts
}

// promptPassword reads a password from the terminal without echo. The
// prompt goes to stderr so stdout stays clean for the command's output.
func promptPassword(ctx context.Context, env *cli.Env, tty *os.File, req PromptRequest) (string, error) {
	if req.Reason != "" {
		fmt.Fprintf(env.Stderr, "remohop: %s\n", req.Reason)
	}
	fmt.Fprintf(env.Stderr, "Password for %s: ", req.Target)
	fd := int(tty.Fd())
	state, err := term.GetState(fd)
	if err != nil {
		return "", err
	}
	type result struct {
		pw  []byte
		err error
	}
	done := make(chan result, 1)
	go func() {
		pw, err := term.ReadPassword(fd)
		done <- result{pw, err}
	}()
	select {
	case r := <-done:
		fmt.Fprintln(env.Stderr)
		if r.err != nil {
			return "", r.err
		}
		return string(r.pw), nil
	case <-ctx.Done():
		// Interrupted while the terminal has echo off: put it back. The
		// reading goroutine stays parked on the terminal until the
		// process exits, which is what follows an interrupt.
		term.Restore(fd, state)
		fmt.Fprintln(env.Stderr)
		return "", errors.New("interrupted")
	}
}
