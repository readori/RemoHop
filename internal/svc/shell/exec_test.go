//go:build !windows

package shell

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestExecStdoutStderrSeparation(t *testing.T) {
	conn := pair(t)
	var out, errb bytes.Buffer
	res, err := Exec(testCtx(t), conn, ExecRequest{
		Command: "echo out1; echo err1 >&2; echo out2; exit 7",
		Stdout:  &out, Stderr: &errb,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "out1\nout2\n" || errb.String() != "err1\n" {
		t.Fatalf("stdout %q stderr %q", out.String(), errb.String())
	}
	if res.ExitCode != 7 || res.Signal != 0 || res.TimedOut {
		t.Fatalf("result %+v", res)
	}
}

func TestExecSignalExit(t *testing.T) {
	conn := pair(t)
	res, err := Exec(testCtx(t), conn, ExecRequest{Command: "kill -TERM $$"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 128+15 || res.Signal != 15 || res.SignalName != "TERM" {
		t.Fatalf("result %+v", res)
	}
}

func TestExecStdinEOF(t *testing.T) {
	conn := pair(t)
	var out bytes.Buffer
	res, err := Exec(testCtx(t), conn, ExecRequest{
		Command: "cat; echo done", Stdin: strings.NewReader("hello\nworld"), Stdout: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.String() != "hello\nworlddone\n" || res.ExitCode != 0 {
		t.Fatalf("out %q res %+v", out.String(), res)
	}
	// nil Stdin is an empty stdin, not a hang.
	out.Reset()
	if _, err := Exec(testCtx(t), conn, ExecRequest{Command: "wc -c", Stdout: &out}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != "0" {
		t.Fatalf("wc -c of empty stdin: %q", out.String())
	}
}

func TestExecTimeout(t *testing.T) {
	conn := pair(t)
	start := time.Now()
	res, err := Exec(testCtx(t), conn, ExecRequest{Command: "echo started; sleep 30", Timeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || res.Signal != 9 {
		t.Fatalf("result %+v", res)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("timeout took %s", d)
	}
}

// readPID reads the first line of r as a PID.
func readPID(t *testing.T, r io.Reader) int {
	t.Helper()
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil {
		t.Fatalf("reading pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("bad pid line %q", line)
	}
	return pid
}

func TestExecCancelKillsProcessGroup(t *testing.T) {
	conn := pair(t)
	ctx, cancel := context.WithCancel(testCtx(t))
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		// A background grandchild in the same process group, plus a
		// foreground wait: both must die.
		_, err := Exec(ctx, conn, ExecRequest{Command: "sleep 1000 & echo $!; wait", Stdout: pw})
		pw.Close()
		errc <- err
	}()
	pid := readPID(t, pr)
	go io.Copy(io.Discard, pr)
	if !procAlive(pid, 0) {
		t.Fatalf("background sleep %d not running", pid)
	}
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("Exec after cancel: %v", err)
	}
	waitFor(t, "grandchild to be killed", 5*time.Second, func() bool { return !procAlive(pid, 0) })
}

func TestExecSessionLossKillsProcessGroup(t *testing.T) {
	conn := pair(t)
	pr, pw := io.Pipe()
	go Exec(context.Background(), conn, ExecRequest{Command: "sleep 1000 & echo $!; wait", Stdout: pw})
	pid := readPID(t, pr)
	go io.Copy(io.Discard, pr)
	conn.Close()
	waitFor(t, "process group to be killed after the session ended", 5*time.Second, func() bool { return !procAlive(pid, 0) })
}

// hashWriter hashes everything written to it.
type hashWriter struct {
	h interface {
		io.Writer
		Sum([]byte) []byte
	}
	n int64
}

func (w *hashWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return w.h.Write(p)
}

func TestExecLargeBinaryOutput(t *testing.T) {
	const size = 64 << 20
	f, err := os.Create(filepath.Join(t.TempDir(), "blob"))
	if err != nil {
		t.Fatal(err)
	}
	want := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(f, want), rand.Reader, size); err != nil {
		t.Fatal(err)
	}
	f.Close()

	conn := pair(t)
	got := &hashWriter{h: sha256.New()}
	res, err := Exec(testCtx(t), conn, ExecRequest{Command: "cat " + f.Name(), Stdout: got})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || got.n != size || !bytes.Equal(got.h.Sum(nil), want.Sum(nil)) {
		t.Fatalf("exit %d, got %d bytes, hash match %v", res.ExitCode, got.n, bytes.Equal(got.h.Sum(nil), want.Sum(nil)))
	}
}

func TestExecBinaryStdin(t *testing.T) {
	data := make([]byte, 8<<20)
	rand.Read(data)
	sum := sha256.Sum256(data)

	conn := pair(t)
	got := &hashWriter{h: sha256.New()}
	res, err := Exec(testCtx(t), conn, ExecRequest{Command: "cat", Stdin: bytes.NewReader(data), Stdout: got})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || !bytes.Equal(got.h.Sum(nil), sum[:]) {
		t.Fatalf("round trip of %d bytes failed (got %d)", len(data), got.n)
	}
}

func TestExecEnvAndCwd(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REMOHOP_TEST_SECRET", "leak")
	conn := pair(t)
	var out bytes.Buffer
	_, err := Exec(testCtx(t), conn, ExecRequest{
		Command: `pwd; echo "$FOO|$BAR|${REMOHOP_TEST_SECRET-unset}|$HOME"`,
		Cwd:     dir, Env: []string{"FOO=bar baz", "BAR=a=b"}, Stdout: &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	gotDir, rest, _ := strings.Cut(out.String(), "\n")
	if !sameDir(gotDir, dir) || rest != "bar baz|a=b|unset|"+os.Getenv("HOME")+"\n" {
		t.Fatalf("got %q (dir %q)", out.String(), dir)
	}

	// Default cwd is home.
	out.Reset()
	Exec(testCtx(t), conn, ExecRequest{Command: "pwd", Stdout: &out})
	if !sameDir(strings.TrimSpace(out.String()), os.Getenv("HOME")) {
		t.Fatalf("default cwd %q", out.String())
	}
}

func TestExecStartError(t *testing.T) {
	conn := pair(t)
	_, err := Exec(testCtx(t), conn, ExecRequest{Command: "true", Cwd: "/nonexistent/dir"})
	var se *StartError
	if !errors.As(err, &se) {
		t.Fatalf("want StartError, got %v", err)
	}
	if _, err := Exec(testCtx(t), conn, ExecRequest{Command: "true", Env: []string{"=x"}}); err == nil {
		t.Fatal("bad env accepted")
	}
}

func TestExecTTY(t *testing.T) {
	conn := pair(t)
	var out bytes.Buffer
	resize := make(chan WinSize, 1)
	res, err := Exec(testCtx(t), conn, ExecRequest{
		Command: "test -t 0 && test -t 1 && echo TTY=$TERM; stty size; exit 3",
		TTY:     true, Term: "vt100", Rows: 33, Cols: 101, Stdout: &out, Resize: resize,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "TTY=vt100") || !strings.Contains(out.String(), "33 101") || res.ExitCode != 3 {
		t.Fatalf("out %q res %+v", out.String(), res)
	}
}

func TestCapture(t *testing.T) {
	conn := pair(t)
	res, err := Capture(testCtx(t), conn, ExecRequest{Command: "head -c 10000 /dev/zero; echo oops >&2; exit 2"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stdout) != 100 || !res.StdoutTruncated || res.StdoutBytes != 10000 {
		t.Fatalf("stdout len %d truncated %v total %d", len(res.Stdout), res.StdoutTruncated, res.StdoutBytes)
	}
	if string(res.Stderr) != "oops\n" || res.StderrTruncated || res.ExitCode != 2 {
		t.Fatalf("stderr %q truncated %v exit %d", res.Stderr, res.StderrTruncated, res.ExitCode)
	}
}

// Output from a background child that keeps the pipes open must not hang
// the request forever.
func TestExecLingeringChild(t *testing.T) {
	old := outputGrace
	outputGrace = 300 * time.Millisecond
	t.Cleanup(func() { outputGrace = old })
	conn := pair(t)
	start := time.Now()
	res, err := Exec(testCtx(t), conn, ExecRequest{Command: "sleep 100 & echo bg"})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != 0 || time.Since(start) > 5*time.Second {
		t.Fatalf("res %+v after %s", res, time.Since(start))
	}
}

// A process that escaped the group (setsid) and holds the PTY must not hang
// a -t command past its timeout.
func TestExecTTYTimeoutWithEscapedHolder(t *testing.T) {
	old := outputGrace
	outputGrace = 300 * time.Millisecond
	t.Cleanup(func() { outputGrace = old })
	pidFile := filepath.Join(t.TempDir(), "pid")
	t.Cleanup(func() {
		if b, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("setsid(1) is not installed")
	}
	conn := pair(t)
	start := time.Now()
	res, err := Exec(testCtx(t), conn, ExecRequest{
		Command: "setsid sh -c 'echo $$ > " + pidFile + "; exec sleep 53' & sleep 100",
		TTY:     true, Timeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TimedOut || time.Since(start) > 6*time.Second {
		t.Fatalf("res %+v after %s", res, time.Since(start))
	}
}
