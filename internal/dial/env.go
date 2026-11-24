package dial

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/term"

	"github.com/readori/RemoHop/internal/cli"
)

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
