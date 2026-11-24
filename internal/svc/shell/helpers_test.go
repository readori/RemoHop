package shell

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

// TestMain gives the whole package a hermetic host environment: a private
// config directory plus the platform's settings (setupTestEnv).
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "remohop-shell-test-")
	if err != nil {
		panic(err)
	}
	setupTestEnv(dir)
	os.Setenv("REMOHOP_CONFIG_DIR", filepath.Join(dir, "config"))
	code := m.Run()
	for _, s := range sessions.list("") {
		sessions.kill(s.Name, "")
	}
	os.RemoveAll(dir)
	os.Exit(code)
}

// syncBuffer is a bytes.Buffer safe for concurrent writers and readers.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// waitFor polls cond until it holds or the timeout expires.
func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitOutput(t *testing.T, buf *syncBuffer, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !strings.Contains(buf.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q; output so far:\n%q", want, buf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func pair(t *testing.T) *peer.Conn {
	client, _ := peertest.Pair(t)
	return client
}

// cliEnv returns an Env wired to conn with captured output.
func cliEnv(conn *peer.Conn, stdin io.Reader) (*cli.Env, *syncBuffer, *syncBuffer) {
	out, errb := &syncBuffer{}, &syncBuffer{}
	env := &cli.Env{
		Target: "123456789", Stdout: out, Stderr: errb,
		DialFunc: func(context.Context, *cli.Env) (*peer.Conn, error) { return conn, nil },
	}
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	env.Stdin = stdin
	return env, out, errb
}

// runCLI runs `remohop 123456789 <args...>` and returns the exit code.
func runCLI(t *testing.T, env *cli.Env, args ...string) int {
	t.Helper()
	c := cli.Lookup(args[0])
	if c == nil {
		t.Fatalf("no command %q", args[0])
	}
	return c.Run(testCtx(t), env, args[1:])
}

// peertestPair returns both ends of a fresh session.
func peertestPair(t *testing.T) (*peer.Conn, *peer.Conn) {
	return peertest.Pair(t)
}

// useJobsDir points the host's job store at a fresh directory for one test.
func useJobsDir(t *testing.T) string {
	dir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", dir)
	return filepath.Join(dir, "jobs")
}

func readLogs(t *testing.T, r *LogReader) string {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading logs: %v (got %q)", err, b)
	}
	return string(b)
}

func findSession(t *testing.T, conn *peer.Conn, name string) *SessionInfo {
	t.Helper()
	list, err := Sessions(testCtx(t), conn)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range list {
		if s.Name == name {
			return &s
		}
	}
	return nil
}

func setKeepAlive(t *testing.T, d time.Duration) {
	old := KeepAlive
	KeepAlive = d
	t.Cleanup(func() { KeepAlive = old })
}

// sameDir compares directories after resolving symlinks (macOS's /var is a
// link to /private/var).
func sameDir(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
}

// longCtx is testCtx for platforms where shells start slowly (PowerShell
// on a CI runner).
func longCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}

var ansiRE = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(\x07|\x1b\\\\)|\x1b[()][0-9A-Za-z]|\x1b[@-Z\\\\-_]")

// plainText strips terminal escape sequences and carriage returns, leaving
// what a user would read (ConPTY output is full of cursor movement).
func plainText(s string) string {
	return strings.ReplaceAll(ansiRE.ReplaceAllString(s, ""), "\r", "")
}

// waitPlain waits until the escape-stripped buffer contains want.
func waitPlain(t *testing.T, buf *syncBuffer, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !strings.Contains(plainText(buf.String()), want) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q; output so far:\n%q", want, plainText(buf.String()))
		}
		time.Sleep(50 * time.Millisecond)
	}
}
