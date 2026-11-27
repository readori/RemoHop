//go:build !windows

package shell

import (
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/readori/RemoHop/internal/peer"
)

// termClient runs `remohop <ID> shell ARGS` on the slave side of a fresh local
// pty pair, the way a user's terminal would, and exposes the master side.
type termClient struct {
	t      *testing.T
	master *os.File
	slave  *os.File
	out    *syncBuffer // everything the CLI wrote to the terminal
	stderr *syncBuffer
	code   chan int
}

func startTermClient(t *testing.T, conn *peer.Conn, rows, cols int, args ...string) *termClient {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("cannot open a local pty pair: %v", err)
	}
	if err := pty.Setsize(slave, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}); err != nil {
		t.Fatal(err)
	}
	tc := &termClient{t: t, master: master, slave: slave, out: &syncBuffer{}, code: make(chan int, 1)}
	go io.Copy(tc.out, master)
	env, _, stderr := cliEnv(conn, slave)
	env.Stdout = slave
	tc.stderr = stderr
	go func() { tc.code <- runCLI(t, env, append([]string{"shell"}, args...)...) }()
	t.Cleanup(tc.close)
	return tc
}

// waitAttached waits until the CLI has attached session name; by then the
// local terminal is in raw mode, so the test's keystrokes are no longer
// echoed locally (which could fake expected output).
func (tc *termClient) waitAttached(conn *peer.Conn, name string) {
	tc.t.Helper()
	waitFor(tc.t, "session "+name+" to be attached", 10*time.Second, func() bool {
		s := findSession(tc.t, conn, name)
		return s != nil && s.Attached
	})
}

func (tc *termClient) close() {
	tc.slave.Close()
	tc.master.Close()
}

func (tc *termClient) typ(s string) {
	tc.t.Helper()
	if _, err := tc.master.Write([]byte(s)); err != nil {
		tc.t.Fatal(err)
	}
}

func (tc *termClient) expect(s string) {
	tc.t.Helper()
	waitOutput(tc.t, tc.out, s, 10*time.Second)
}

func (tc *termClient) exitCode() int {
	tc.t.Helper()
	select {
	case c := <-tc.code:
		return c
	case <-time.After(10 * time.Second):
		tc.t.Fatalf("remohop shell did not exit; stderr: %s", tc.stderr.String())
		return -1
	}
}

func TestInteractiveDetachReattachResizeExit(t *testing.T) {
	conn := pair(t)

	// First attach: run something, then detach with the escape.
	tc := startTermClient(t, conn, 24, 80, "-s", "t1")
	tc.waitAttached(conn, "t1")
	tc.typ("echo hello-$((6*7))\r")
	tc.expect("hello-42")
	tc.typ("\r~.")
	if c := tc.exitCode(); c != 0 {
		t.Fatalf("detach exit code %d; stderr %s", c, tc.stderr.String())
	}
	if !strings.Contains(tc.stderr.String(), `detached from session "t1"`) ||
		!strings.Contains(tc.stderr.String(), "remohop 123456789 shell -s t1") {
		t.Fatalf("detach hint: %q", tc.stderr.String())
	}
	tc.close()

	// The session survives, detached. The host learns of the detach when
	// the attach stream closes, a moment after the client has exited.
	waitFor(t, "the host to see the detach", 5*time.Second, func() bool {
		s := findSession(t, conn, "t1")
		return s != nil && !s.Attached
	})
	s := findSession(t, conn, "t1")
	if s == nil || s.Attached || s.ExpiresInSeconds <= 0 {
		t.Fatalf("session after detach: %+v", s)
	}

	// Re-attach from a new terminal: the old output is replayed.
	tc2 := startTermClient(t, conn, 24, 80, "-s", "t1")
	tc2.waitAttached(conn, "t1")
	tc2.expect("hello-42")

	// Window size changes propagate.
	pty.Setsize(tc2.slave, &pty.Winsize{Rows: 40, Cols: 120})
	syscall.Kill(os.Getpid(), syscall.SIGWINCH)
	waitFor(t, "resize to reach the host", 5*time.Second, func() bool {
		s := findSession(t, conn, "t1")
		return s != nil && s.Rows == 40 && s.Cols == 120
	})
	tc2.typ("stty size\r")
	tc2.expect("40 120")

	// The shell's exit code becomes the CLI's, and the session is gone.
	tc2.typ("exit 3\r")
	if c := tc2.exitCode(); c != 3 {
		t.Fatalf("exit code %d, want 3", c)
	}
	if s := findSession(t, conn, "t1"); s != nil {
		t.Fatalf("session still listed after exit: %+v", s)
	}
}

func TestInteractiveSurvivesConnectionLoss(t *testing.T) {
	client1, _ := peertestPair(t)
	tc := startTermClient(t, client1, 24, 80, "-s", "t2")
	tc.waitAttached(client1, "t2")
	tc.typ("X=persisted\r")
	tc.typ("echo marker$((1+1))\r")
	tc.expect("marker2")
	client1.Close()
	if c := tc.exitCode(); c != 255 {
		t.Fatalf("exit code after connection loss %d", c)
	}
	if !strings.Contains(tc.stderr.String(), `session "t2" is detached`) {
		t.Fatalf("hint: %q", tc.stderr.String())
	}

	conn := pair(t)
	waitFor(t, "session to notice the lost client", 5*time.Second, func() bool {
		s := findSession(t, conn, "t2")
		return s != nil && !s.Attached
	})
	tc2 := startTermClient(t, conn, 24, 80, "-s", "t2")
	tc2.waitAttached(conn, "t2")
	tc2.typ("echo value=$X\r")
	tc2.expect("value=persisted")
	tc2.typ("exit\r")
	if c := tc2.exitCode(); c != 0 {
		t.Fatalf("exit code %d", c)
	}
}

func TestKeepAliveExpiry(t *testing.T) {
	setKeepAlive(t, 300*time.Millisecond)
	conn := pair(t)
	tc := startTermClient(t, conn, 24, 80, "-s", "ka")
	tc.waitAttached(conn, "ka")
	tc.typ("echo u$((0))p\r")
	tc.expect("u0p")
	tc.typ("\r~.")
	tc.exitCode()
	waitFor(t, "detached session to be reaped", 10*time.Second, func() bool { return findSession(t, conn, "ka") == nil })
}

func TestKeepAliveZeroKillsOnDetach(t *testing.T) {
	setKeepAlive(t, 0)
	conn := pair(t)
	tc := startTermClient(t, conn, 24, 80, "-s", "ka0")
	tc.waitAttached(conn, "ka0")
	tc.typ("echo u$((0))p\r")
	tc.expect("u0p")
	tc.typ("\r~.")
	tc.exitCode()
	if !strings.Contains(tc.stderr.String(), "ends detached sessions immediately") {
		t.Fatalf("hint: %q", tc.stderr.String())
	}
	waitFor(t, "session to end", 10*time.Second, func() bool { return findSession(t, conn, "ka0") == nil })
}

func TestTakeover(t *testing.T) {
	conn := pair(t)
	ctx := testCtx(t)
	in1, w1 := io.Pipe()
	defer w1.Close()
	out1 := &syncBuffer{}
	done1 := make(chan *AttachResult, 1)
	go func() {
		res, err := Attach(ctx, conn, AttachRequest{Name: "take"}, AttachIO{Stdin: in1, Stdout: out1, Escape: '~'})
		if err != nil {
			t.Error(err)
		}
		done1 <- res
	}()
	w1.Write([]byte("echo first-$((1))-client\r"))
	waitOutput(t, out1, "first-1-client", 10*time.Second)

	in2, w2 := io.Pipe()
	out2 := &syncBuffer{}
	done2 := make(chan *AttachResult, 1)
	go func() {
		res, err := Attach(ctx, conn, AttachRequest{Name: "take"}, AttachIO{Stdin: in2, Stdout: out2})
		if err != nil {
			t.Error(err)
		}
		done2 <- res
	}()
	select {
	case res := <-done1:
		if res == nil || res.Detached != DetachTakeover {
			t.Fatalf("first client: %+v", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("first client not detached by takeover")
	}
	waitOutput(t, out2, "first-1-client", 10*time.Second) // replay
	w2.Write([]byte("exit 5\r"))
	select {
	case res := <-done2:
		if res == nil || !res.Exited || res.ExitCode != 5 || res.Created {
			t.Fatalf("second client: %+v", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("second client did not see the exit")
	}
}

func TestNewSessionAndKill(t *testing.T) {
	conn := pair(t)
	ctx := testCtx(t)
	attach := func(req AttachRequest) (*AttachResult, *io.PipeWriter, chan *AttachResult) {
		in, w := io.Pipe()
		started := make(chan *AttachResult, 1)
		done := make(chan *AttachResult, 1)
		go func() {
			res, err := Attach(ctx, conn, req, AttachIO{Stdin: in, OnAttach: func(r *AttachResult) { started <- r }})
			if err != nil {
				started <- nil
			}
			done <- res
		}()
		return <-started, w, done
	}
	a, wa, _ := attach(AttachRequest{Name: "dup"})
	if a == nil || a.Session != "dup" || !a.Created {
		t.Fatalf("first: %+v", a)
	}
	b, wb, doneB := attach(AttachRequest{Name: "dup", New: true})
	if b == nil || b.Session != "dup-2" || !b.Created {
		t.Fatalf("--new: %+v", b)
	}
	// Closing stdin detaches; the session keeps running.
	wa.Close()

	env, out, _ := cliEnv(conn, nil)
	if c := runCLI(t, env, "shell", "kill", "dup-2"); c != 0 {
		t.Fatalf("shell kill exit %d", c)
	}
	select {
	case res := <-doneB:
		// SIGHUP: 128+1.
		if !res.Exited || res.ExitCode != 129 {
			t.Fatalf("killed session result %+v", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("attached client did not see the kill")
	}
	wb.Close()

	if c := runCLI(t, env, "shell", "ls"); c != 0 || !strings.Contains(out.String(), "dup ") {
		t.Fatalf("shell ls: %d %q", c, out.String())
	}
	if c := runCLI(t, env, "shell", "kill", "nope"); c != 1 {
		t.Fatalf("killing a missing session: exit %d", c)
	}
	KillSession(ctx, conn, "dup")
	waitFor(t, "session dup to end", 10*time.Second, func() bool { return findSession(t, conn, "dup") == nil })
}

func TestInteractiveRequiresTerminal(t *testing.T) {
	conn := pair(t)
	env, _, errb := cliEnv(conn, strings.NewReader("echo hi\n"))
	if c := runCLI(t, env, "shell"); c != 2 || !strings.Contains(errb.String(), "not a terminal") {
		t.Fatalf("exit %d stderr %q", c, errb.String())
	}
}

// A background job still holding the terminal must not keep a session
// alive after its shell exits: the client gets the exit code and the session
// is removed. The job itself keeps running (as after a normal logout).
func TestSessionEndsDespiteBackgroundJob(t *testing.T) {
	conn := pair(t)
	ctx := testCtx(t)
	in, w := io.Pipe()
	defer w.Close()
	out := &syncBuffer{}
	done := make(chan *AttachResult, 1)
	go func() {
		res, err := Attach(ctx, conn, AttachRequest{Name: "bgjob"}, AttachIO{Stdin: in, Stdout: out})
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	w.Write([]byte("sleep 47 & echo bg=$!=\r"))
	// The command line itself is echoed (sometimes twice, by the terminal
	// and again by the shell's line editor), so wait for a real number.
	bgPID := regexp.MustCompile(`bg=([0-9]+)=`)
	waitFor(t, "background pid", 10*time.Second, func() bool { return bgPID.MatchString(out.String()) })
	var pid int
	s := out.String()
	if m := bgPID.FindStringSubmatch(s); m != nil {
		pid, _ = strconv.Atoi(m[1])
	}
	if pid <= 0 {
		t.Fatalf("no pid in %q", s)
	}
	t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	w.Write([]byte("exit 3\r"))
	select {
	case res := <-done:
		if res == nil || !res.Exited || res.ExitCode != 3 {
			t.Fatalf("result %+v", res)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("client never got the exit while a background job held the terminal")
	}
	waitFor(t, "session removal", 5*time.Second, func() bool { return findSession(t, conn, "bgjob") == nil })
	if !procAlive(pid, 0) {
		t.Fatal("background job was killed; it should outlive the session like after a logout")
	}
}
