package shell

import (
	"bytes"
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Windows tests drive the real host shell (PowerShell on every supported
// Windows) through a loopback session, exactly like the unix tests.

func crlf(s string) string { return strings.ReplaceAll(s, "\r\n", "\n") }

func winExec(t *testing.T, req ExecRequest) (*ExecResult, string, string) {
	t.Helper()
	conn := pair(t)
	var out, errb bytes.Buffer
	req.Stdout, req.Stderr = &out, &errb
	res, err := Exec(longCtx(t), conn, req)
	if err != nil {
		t.Fatalf("Exec(%q): %v (stdout %q stderr %q)", req.Command, err, out.String(), errb.String())
	}
	return res, crlf(out.String()), crlf(errb.String())
}

func TestWinShellIsPowerShell(t *testing.T) {
	if k := kindOf(userShell()); k != shellPowerShell {
		t.Fatalf("default shell %q is not PowerShell", userShell())
	}
}

func TestWinExecStdoutStderrExit(t *testing.T) {
	res, out, errs := winExec(t, ExecRequest{
		Command: "Write-Output out1; [Console]::Error.WriteLine('err1'); Write-Output out2; exit 7",
	})
	if out != "out1\nout2\n" || errs != "err1\n" || res.ExitCode != 7 {
		t.Fatalf("stdout %q stderr %q exit %d", out, errs, res.ExitCode)
	}
}

func TestWinExecExitCodes(t *testing.T) {
	for _, c := range []struct {
		cmd  string
		code int
	}{
		{"exit 7", 7},
		{"Write-Output x; exit 0", 0},
		{"cmd /c exit 5", 5},                    // failing native command last
		{"cmd /c exit 5; Write-Output ok", 0},   // last statement succeeded
		{"throw 'boom'", 1},                     // terminating error
		{"Get-Item 'C:\\no\\such\\path\\x'", 1}, // failing cmdlet
		{"$x = 1", 0},
	} {
		res, out, errs := winExec(t, ExecRequest{Command: c.cmd})
		if res.ExitCode != c.code {
			t.Errorf("%q: exit %d, want %d (stdout %q stderr %q)", c.cmd, res.ExitCode, c.code, out, errs)
		}
	}
}

func TestWinExecStdin(t *testing.T) {
	res, out, _ := winExec(t, ExecRequest{
		Command: `$s = [Console]::In.ReadToEnd(); Write-Output ("[" + $s + "]")`,
		Stdin:   strings.NewReader("hello\nw\u00f6rld"),
	})
	if out != "[hello\nw\u00f6rld]\n" || res.ExitCode != 0 {
		t.Fatalf("stdout %q exit %d", out, res.ExitCode)
	}
}

func TestWinExecUTF8(t *testing.T) {
	_, out, _ := winExec(t, ExecRequest{
		Command: "Write-Output ([string][char]0x00e9 + [char]0x4e2d + [char]0x0444); Write-Output '\u00e9\u4e2d'",
	})
	if out != "\u00e9\u4e2d\u0444\n\u00e9\u4e2d\n" {
		t.Fatalf("stdout %q (% x)", out, out)
	}
}

func TestWinExecCwdEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REMOHOP_TEST_SECRET", "leak")
	_, out, _ := winExec(t, ExecRequest{
		Command: `(Get-Location).Path; $env:FOO; if ($env:REMOHOP_TEST_SECRET) { 'leak' } else { 'unset' }`,
		Cwd:     dir, Env: []string{"FOO=bar baz"},
	})
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 || !sameDir(lines[0], dir) || lines[1] != "bar baz" || lines[2] != "unset" {
		t.Fatalf("stdout %q (dir %q)", out, dir)
	}
	_, out, _ = winExec(t, ExecRequest{Command: `(Get-Location).Path`})
	if !sameDir(strings.TrimSpace(out), homeDir()) {
		t.Fatalf("default cwd %q, home %q", out, homeDir())
	}
}

func TestWinExecStartError(t *testing.T) {
	conn := pair(t)
	_, err := Exec(longCtx(t), conn, ExecRequest{Command: "exit 0", Cwd: `C:\no\such\dir`})
	var se *StartError
	if !errors.As(err, &se) {
		t.Fatalf("want StartError, got %v", err)
	}
}

func TestWinExecLargeOutput(t *testing.T) {
	_, out, _ := winExec(t, ExecRequest{
		Command: "[Console]::Out.Write(('x' * 4194304)); [Console]::Out.Write('END'); [Console]::Out.Flush()",
	})
	if len(out) != 4194304+3 || !strings.HasSuffix(out, "xEND") {
		t.Fatalf("got %d bytes, suffix %q", len(out), out[max(0, len(out)-10):])
	}
}

func TestWinCapture(t *testing.T) {
	conn := pair(t)
	res, err := Capture(longCtx(t), conn, ExecRequest{Command: "[Console]::Out.Write(('a' * 5000)); exit 2"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stdout) != 100 || !res.StdoutTruncated || res.StdoutBytes != 5000 || res.ExitCode != 2 {
		t.Fatalf("%+v", res)
	}
}

func TestWinExecTimeout(t *testing.T) {
	start := time.Now()
	res, _, _ := winExec(t, ExecRequest{Command: "Start-Sleep 60", Timeout: 2 * time.Second})
	if !res.TimedOut {
		t.Fatalf("result %+v", res)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Fatalf("timeout took %s", d)
	}
}

// pidWriter collects output and reports the first "PID=n" it sees.
type pidWriter struct {
	mu  sync.Mutex
	buf bytes.Buffer
	pid chan int
}

var pidRE = regexp.MustCompile(`PID=(\d+)`)

func (w *pidWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	had := pidRE.Match(w.buf.Bytes())
	w.buf.Write(p)
	if m := pidRE.FindSubmatch(w.buf.Bytes()); !had && m != nil {
		n, _ := strconv.Atoi(string(m[1]))
		w.pid <- n
	}
	return len(p), nil
}

// grandchild is a PowerShell command that starts a long-lived child process
// and reports its PID; killing the command must kill that child too.
const grandchild = `$p = Start-Process -FilePath ping.exe -ArgumentList '-n','600','127.0.0.1' -NoNewWindow -PassThru
[Console]::Out.WriteLine("PID=" + $p.Id); [Console]::Out.Flush()
Start-Sleep 600`

func startGrandchild(t *testing.T, ctx context.Context, req ExecRequest) (int, chan error) {
	t.Helper()
	conn := pair(t)
	w := &pidWriter{pid: make(chan int, 1)}
	req.Command, req.Stdout = grandchild, w
	errc := make(chan error, 1)
	go func() {
		_, err := Exec(ctx, conn, req)
		errc <- err
	}()
	select {
	case pid := <-w.pid:
		if !procAlive(pid, 0) {
			t.Fatalf("child %d not running", pid)
		}
		return pid, errc
	case err := <-errc:
		t.Fatalf("command ended early: %v; output %q", err, w.buf.String())
	case <-time.After(60 * time.Second):
		t.Fatal("no PID from the command")
	}
	return 0, nil
}

func TestWinExecTimeoutKillsTree(t *testing.T) {
	pid, errc := startGrandchild(t, longCtx(t), ExecRequest{Timeout: 5 * time.Second})
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	waitFor(t, "grandchild to be killed", 20*time.Second, func() bool { return !procAlive(pid, 0) })
}

func TestWinExecCancelKillsTree(t *testing.T) {
	ctx, cancel := context.WithCancel(longCtx(t))
	pid, errc := startGrandchild(t, ctx, ExecRequest{})
	cancel()
	if err := <-errc; !errors.Is(err, context.Canceled) {
		t.Fatalf("Exec after cancel: %v", err)
	}
	waitFor(t, "grandchild to be killed", 20*time.Second, func() bool { return !procAlive(pid, 0) })
}

func TestWinExecSessionLossKillsTree(t *testing.T) {
	conn := pair(t)
	w := &pidWriter{pid: make(chan int, 1)}
	go Exec(context.Background(), conn, ExecRequest{Command: grandchild, Stdout: w})
	var pid int
	select {
	case pid = <-w.pid:
	case <-time.After(60 * time.Second):
		t.Fatal("no PID from the command")
	}
	conn.Close()
	waitFor(t, "grandchild to be killed after the session ended", 20*time.Second, func() bool { return !procAlive(pid, 0) })
}

func TestWinExecCmdFallback(t *testing.T) {
	t.Setenv("REMOHOP_SHELL", "cmd.exe")
	if kindOf(userShell()) != shellCmd {
		t.Fatalf("REMOHOP_SHELL ignored: %q", userShell())
	}
	res, out, _ := winExec(t, ExecRequest{Command: "echo hi& exit /b 3"})
	if out != "hi\n" || res.ExitCode != 3 {
		t.Fatalf("stdout %q exit %d", out, res.ExitCode)
	}
}

func TestWinExecTTY(t *testing.T) {
	res, out, _ := winExec(t, ExecRequest{
		Command: `Write-Output ("t" + "ty-ok"); Write-Output ("W=" + $Host.UI.RawUI.WindowSize.Width + "H=" + $Host.UI.RawUI.WindowSize.Height); exit 4`,
		TTY:     true, Rows: 30, Cols: 100,
	})
	plain := plainText(out)
	if !strings.Contains(plain, "tty-ok") || !strings.Contains(plain, "W=100H=30") || res.ExitCode != 4 {
		t.Fatalf("output %q exit %d", plain, res.ExitCode)
	}
}

func TestWinCLIExec(t *testing.T) {
	conn := pair(t)
	env, out, errb := cliEnv(conn, strings.NewReader(""))
	if code := runCLI(t, env, "shell", "-c", "Write-Output hi; [Console]::Error.WriteLine('E'); exit 5"); code != 5 ||
		crlf(out.String()) != "hi\n" || crlf(errb.String()) != "E\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, out.String(), errb.String())
	}
	env, _, errb = cliEnv(conn, nil)
	if code := runCLI(t, env, "shell", "-c", "Start-Sleep 60", "--timeout", "2s"); code != exitTimeout {
		t.Fatalf("timeout exit %d stderr %q", code, errb.String())
	}
}

// How piped stdin reaches a PowerShell command. The automatic $input
// variable and [Console]::In (TestWinExecStdin) both read it, and $input
// can be piped on to a native program.
func TestWinExecStdinForms(t *testing.T) {
	const in = "alpha\nxbeta\ngamma x\n"
	for _, c := range []struct{ name, cmd, want string }{
		{"$input", `$input | ForEach-Object { "<$_>" }`, "<alpha>\n<xbeta>\n<gamma x>\n"},
		{"$input to native", `$input | findstr.exe x`, "xbeta\ngamma x\n"},
		{"@($input).Count", `@($input).Count`, "3\n"},
	} {
		res, out, errs := winExec(t, ExecRequest{Command: c.cmd, Stdin: strings.NewReader(in)})
		if out != c.want || res.ExitCode != 0 {
			t.Errorf("%s: stdout %q exit %d stderr %q, want %q", c.name, out, res.ExitCode, errs, c.want)
		}
	}
}

// A native program started by the script without an explicit pipe: whether
// it sees the piped stdin depends on PowerShell not having claimed it for
// $input. The documented, portable form is "$input | program" (tested
// above); this records what the bare form does on the host under test.
func TestWinExecStdinBareNative(t *testing.T) {
	for _, cmd := range []string{"findstr.exe x", "sort.exe", "more.com"} {
		res, out, errs := winExec(t, ExecRequest{Command: cmd, Stdin: strings.NewReader("alpha\nxbeta\n"), Timeout: 20 * time.Second})
		t.Logf("bare native %q with piped stdin: stdout %q exit %d timedOut %v stderr %q", cmd, out, res.ExitCode, res.TimedOut, errs)
	}
}

// Errors must reach stderr as plain text: not CLIXML (what -EncodedCommand
// defaults to) and without colour escapes.
func TestWinExecErrorsArePlainText(t *testing.T) {
	for _, cmd := range []string{"Write-Error 'oops-err'", "Get-Item 'C:\\no\\such\\path\\x'", "echo a >&2"} {
		res, out, errs := winExec(t, ExecRequest{Command: cmd})
		if strings.Contains(errs, "CLIXML") || strings.Contains(errs, "\x1b") || strings.Contains(errs, "_x001B_") ||
			strings.TrimSpace(errs) == "" || res.ExitCode == 0 {
			t.Errorf("%q: exit %d stdout %q stderr %q", cmd, res.ExitCode, out, errs)
		}
	}
	_, _, errs := winExec(t, ExecRequest{Command: "Write-Error 'oops-err'"})
	if !strings.Contains(errs, "oops-err") {
		t.Errorf("stderr %q", errs)
	}
}
