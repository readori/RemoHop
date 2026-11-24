package shell

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/peer"
)

// winClient is an attached session driven through pipes (the CI runner has
// no console to put in raw mode; the terminal side is the host's ConPTY).
type winClient struct {
	t      *testing.T
	in     *io.PipeWriter
	out    *syncBuffer
	resize chan WinSize
	done   chan *AttachResult
}

func attachWin(t *testing.T, conn *peer.Conn, req AttachRequest) *winClient {
	t.Helper()
	in, w := io.Pipe()
	c := &winClient{t: t, in: w, out: &syncBuffer{}, resize: make(chan WinSize, 1), done: make(chan *AttachResult, 1)}
	ctx := longCtx(t)
	go func() {
		res, err := Attach(ctx, conn, req, AttachIO{Stdin: in, Stdout: c.out, Resize: c.resize, Escape: '~'})
		if err != nil {
			t.Errorf("Attach: %v", err)
		}
		c.done <- res
	}()
	t.Cleanup(func() { w.Close() })
	return c
}

func (c *winClient) typ(s string) {
	c.t.Helper()
	if _, err := c.in.Write([]byte(s)); err != nil {
		c.t.Fatal(err)
	}
}

// expect waits for want in the escape-stripped output, retyping cmd (if
// any) every few seconds: a shell still loading its profile may drop
// early keystrokes.
func (c *winClient) expect(cmd, want string) {
	c.t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for {
		if cmd != "" {
			c.typ(cmd)
		}
		next := time.Now().Add(5 * time.Second)
		for time.Now().Before(next) {
			if strings.Contains(plainText(c.out.String()), want) {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("timed out waiting for %q; output:\n%q", want, plainText(c.out.String()))
		}
	}
}

func (c *winClient) result() *AttachResult {
	c.t.Helper()
	select {
	case r := <-c.done:
		if r == nil {
			c.t.Fatal("attach failed")
		}
		return r
	case <-time.After(60 * time.Second):
		c.t.Fatalf("attach did not end; output:\n%q", plainText(c.out.String()))
		return nil
	}
}

func TestWinSessionLifecycle(t *testing.T) {
	conn := pair(t)
	c := attachWin(t, conn, AttachRequest{Name: "w1", Rows: 25, Cols: 90})
	c.expect("Write-Output (\"hel\" + \"lo-42\")\r", "hello-42")

	c.resize <- WinSize{Rows: 30, Cols: 100}
	c.expect("Write-Output (\"SZ=\" + $Host.UI.RawUI.WindowSize.Width + \"x\" + $Host.UI.RawUI.WindowSize.Height)\r", "SZ=100x30")

	// Detach by closing input; the session keeps running.
	c.in.Close()
	if r := c.result(); r.Detached != DetachInputClosed || r.Session != "w1" {
		t.Fatalf("detach result %+v", r)
	}
	waitFor(t, "session to be detached", 30*time.Second, func() bool {
		s := findSession(t, conn, "w1")
		return s != nil && !s.Attached
	})

	// Re-attach: the earlier output is replayed.
	c2 := attachWin(t, conn, AttachRequest{Name: "w1", Rows: 30, Cols: 100})
	c2.expect("", "hello-42")
	c2.typ("exit 3\r")
	if r := c2.result(); !r.Exited || r.ExitCode != 3 || r.Created {
		t.Fatalf("exit result %+v", r)
	}
	waitFor(t, "session removal", 30*time.Second, func() bool { return findSession(t, conn, "w1") == nil })
}

func TestWinSessionKill(t *testing.T) {
	conn := pair(t)
	c := attachWin(t, conn, AttachRequest{Name: "wk"})
	c.expect("Write-Output (\"rea\" + \"dy\")\r", "ready")
	pid := findSession(t, conn, "wk").PID
	if err := KillSession(longCtx(t), conn, "wk"); err != nil {
		t.Fatal(err)
	}
	if r := c.result(); !r.Exited {
		t.Fatalf("kill result %+v", r)
	}
	waitFor(t, "session removal", 30*time.Second, func() bool { return findSession(t, conn, "wk") == nil })
	waitFor(t, "shell process to end", 30*time.Second, func() bool { return !procAlive(pid, 0) })
}

func TestWinSessionKeepAliveExpiry(t *testing.T) {
	setKeepAlive(t, time.Second)
	conn := pair(t)
	c := attachWin(t, conn, AttachRequest{Name: "wka"})
	c.expect("Write-Output (\"rea\" + \"dy\")\r", "ready")
	c.in.Close()
	c.result()
	waitFor(t, "detached session to be reaped", 60*time.Second, func() bool { return findSession(t, conn, "wka") == nil })
}

func TestWinSessionTakeover(t *testing.T) {
	conn := pair(t)
	c := attachWin(t, conn, AttachRequest{Name: "wt"})
	c.expect("Write-Output (\"fir\" + \"st\")\r", "first")
	c2 := attachWin(t, conn, AttachRequest{Name: "wt"})
	if r := c.result(); r.Detached != DetachTakeover {
		t.Fatalf("first client %+v", r)
	}
	c2.expect("", "first")
	c2.typ("exit 0\r")
	if r := c2.result(); !r.Exited || r.ExitCode != 0 {
		t.Fatalf("second client %+v", r)
	}
}

func TestWinSessionCmdShell(t *testing.T) {
	t.Setenv("REMOHOP_SHELL", "cmd.exe")
	conn := pair(t)
	c := attachWin(t, conn, AttachRequest{Name: "wcmd"})
	c.expect("set /a 6*7\r", "42")
	c.typ("exit 3\r")
	if r := c.result(); !r.Exited || r.ExitCode != 3 {
		t.Fatalf("result %+v", r)
	}
}
