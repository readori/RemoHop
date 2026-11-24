//go:build unix

package hostd_test

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/svc/forward"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
	"github.com/readori/RemoHop/internal/svc/shell"
)

// revokeWithin bounds how long an open stream may outlive its feature.
const revokeWithin = 3 * time.Second

// processGone waits until pid no longer exists.
func processGone(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(revokeWithin + 2*time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("%s (pid %d) still runs after its feature was switched off", what, pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitErr(t *testing.T, ch <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-ch:
		return err
	case <-time.After(revokeWithin):
		t.Fatalf("%s outlived its feature being switched off", what)
	}
	return nil
}

// Switching a feature off ends what a connected client already has open
// with it, not only what it opens next: a running command and terminal,
// a file upload in progress, a forwarded connection.
func TestFeatureOffEndsOpenStreams(t *testing.T) {
	h, _ := ownedHost(t, hostd.Config{})
	conn, err := dial.Dial(testCtx(t), h.Addr().String(), dial.Options{Dir: t.TempDir(), Password: h.Password(), Locals: noLocals})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx := testCtx(t)

	// shell: a command that runs until stopped, and an interactive terminal.
	old := shell.KeepAlive
	shell.KeepAlive = time.Hour
	t.Cleanup(func() { shell.KeepAlive = old })
	outR, outW := io.Pipe()
	execDone := make(chan error, 1)
	go func() {
		_, err := shell.Exec(ctx, conn, shell.ExecRequest{Command: "echo $$; while :; do sleep 0.1; done", Stdout: outW})
		execDone <- err
	}()
	line, err := bufio.NewReader(outR).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	go io.Copy(io.Discard, outR)
	execPID, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("pid line %q", line)
	}
	termIn, _ := io.Pipe()
	attached := make(chan *shell.AttachResult, 1)
	termDone := make(chan error, 1)
	var termRes *shell.AttachResult
	go func() {
		r, err := shell.Attach(ctx, conn, shell.AttachRequest{Name: "revoke-test", Rows: 24, Cols: 80},
			shell.AttachIO{Stdin: termIn, Stdout: io.Discard, OnAttach: func(r *shell.AttachResult) { attached <- r }})
		termRes = r
		termDone <- err
	}()
	var termPID int
	select {
	case r := <-attached:
		termPID = r.PID
	case <-time.After(10 * time.Second):
		t.Fatal("terminal did not attach")
	}

	// file: an upload that is still sending.
	fc, err := rfs.NewClient(ctx, conn)
	if err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "upload.bin")
	upDone := make(chan error, 1)
	sent := make(chan struct{}, 1)
	go func() {
		_, err := fc.Upload(ctx, dest, &slowReader{sent: sent}, rfs.PutOptions{})
		upDone <- err
	}()
	<-sent

	// forward: a connection to a local echo server.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	fwd, err := forward.Dial(ctx, conn, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer fwd.Close()
	if _, err := fwd.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(fwd, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("echo through the forward: %q %v", buf, err)
	}
	fwdDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, fwd)
		fwdDone <- err
	}()

	// Switching shell off ends the command (its process group is killed)
	// and the terminal (the shell is ended, not kept for re-attaching);
	// file transfers and forwards are not touched by it.
	set(t, hostd.FeatureSetting(perm.Shell), "off")
	if err := waitErr(t, execDone, "a running command"); err == nil {
		t.Fatal("the command was reported as finishing normally")
	}
	processGone(t, execPID, "the command")
	waitErr(t, termDone, "an attached terminal")
	if termRes == nil || !termRes.Exited {
		t.Fatalf("terminal result %+v: the shell must be ended, not detached", termRes)
	}
	processGone(t, termPID, "the terminal's shell")
	select {
	case err := <-upDone:
		t.Fatalf("the upload ended with the shell feature: %v", err)
	case err := <-fwdDone:
		t.Fatalf("the forward ended with the shell feature: %v", err)
	default:
	}

	// file off: the upload stops and leaves no file behind.
	set(t, hostd.FeatureSetting(perm.File), "off")
	if err := waitErr(t, upDone, "an upload in progress"); err == nil {
		t.Fatal("the upload was reported as complete")
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the interrupted upload left %s behind: %v", dest, err)
	}
	select {
	case <-fc.Done():
	case <-time.After(revokeWithin):
		t.Fatal("the file session outlived the file feature")
	}

	// forward off: the forwarded connection is cut.
	set(t, hostd.FeatureSetting(perm.Forward), "off")
	waitErr(t, fwdDone, "a forwarded connection")

	// Back on, new streams work again.
	set(t, hostd.FeatureSetting(perm.Forward), "on")
	if c, err := forward.Dial(ctx, conn, "tcp", ln.Addr().String()); err != nil {
		t.Fatalf("forward after switching it back on: %v", err)
	} else {
		c.Close()
	}
}

// slowReader produces data forever, a little at a time, like a large file
// on a slow link.
type slowReader struct {
	sent chan struct{}
	n    int
}

func (r *slowReader) Read(p []byte) (int, error) {
	if r.n > 0 {
		time.Sleep(20 * time.Millisecond)
	}
	r.n++
	n := min(len(p), 32<<10)
	for i := range p[:n] {
		p[i] = 'x'
	}
	if r.n == 4 {
		select {
		case r.sent <- struct{}{}:
		default:
		}
	}
	return n, nil
}
