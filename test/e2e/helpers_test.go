// Package e2e drives the real remohop binary through features that need a real
// environment: a real SMTP server for accounts, a real Cloudflare Tunnel in
// front of `remohop web`. Every test skips unless its REMOHOP_*_E2E variable is
// set to 1; the shell drivers next to this file cover what needs root
// (systemd, kernel modules).
//
// The binary is built once per run, or taken from REMOHOP_E2E_BIN.
package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

var (
	buildOnce   sync.Once
	buildDir    string
	remohopPath string
	buildErr    error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if buildDir != "" {
		os.RemoveAll(buildDir)
	}
	os.Exit(code)
}

// remohopBin returns the remohop binary under test.
func remohopBin(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		if p := os.Getenv("REMOHOP_E2E_BIN"); p != "" {
			remohopPath, buildErr = filepath.Abs(p)
			return
		}
		if buildDir, buildErr = os.MkdirTemp("", "remohop-e2e-bin"); buildErr != nil {
			return
		}
		remohopPath = filepath.Join(buildDir, "remohop")
		if runtime.GOOS == "windows" {
			remohopPath += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", remohopPath, "github.com/readori/RemoHop/cmd/remohop")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = fmt.Errorf("go build: %v\n%s", err, out)
		}
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return remohopPath
}

// device is one remohop installation: its own config directory.
type device struct {
	t    *testing.T
	name string
	dir  string
	env  []string // extra environment
}

func newDevice(t *testing.T, name string) *device {
	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return &device{t: t, name: name, dir: dir}
}

func (d *device) environ(extra ...string) []string {
	env := []string{}
	for _, kv := range os.Environ() {
		// Nothing of the caller's remohop setup leaks into a device.
		if strings.HasPrefix(kv, "REMOHOP_") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "REMOHOP_CONFIG_DIR="+d.dir)
	env = append(env, d.env...)
	return append(env, extra...)
}

type result struct {
	stdout, stderr string
	code           int
}

func (r result) String() string {
	return fmt.Sprintf("exit %d\nstdout: %s\nstderr: %s", r.code, clip(r.stdout), clip(r.stderr))
}

// redacted are strings never to print (a tunnel's public host name);
// clip replaces them.
var (
	redactMu sync.Mutex
	redacted = map[string]string{}
)

func redact(secret, label string) {
	redactMu.Lock()
	redacted[secret] = label
	redactMu.Unlock()
}

func clip(s string) string {
	redactMu.Lock()
	for k, v := range redacted {
		s = strings.ReplaceAll(s, k, v)
	}
	redactMu.Unlock()
	s = strings.TrimSpace(s)
	if len(s) > 2000 {
		s = s[:2000] + "..."
	}
	return s
}

// run runs remohop on the device and waits (at most two minutes).
func (d *device) run(args ...string) result { return d.runEnv(nil, args...) }

func (d *device) runEnv(extra []string, args ...string) result {
	d.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, remohopBin(d.t), args...)
	cmd.Env = d.environ(extra...)
	cmd.Stdin = strings.NewReader("") // never a terminal: nothing may prompt
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	r := result{stdout: out.String(), stderr: errb.String()}
	if ee, ok := err.(*exec.ExitError); ok {
		r.code = ee.ExitCode()
	} else if err != nil {
		d.t.Fatalf("%s: remohop %s: %v", d.name, args[0], err)
	}
	return r
}

// must runs remohop and fails the test unless it exits 0.
func (d *device) must(args ...string) result {
	d.t.Helper()
	r := d.run(args...)
	if r.code != 0 {
		d.t.Fatalf("%s: remohop %s: %s", d.name, strings.Join(redactArgs(args), " "), r)
	}
	return r
}

// redactArgs hides the value after flags that carry secrets.
func redactArgs(args []string) []string {
	out := append([]string(nil), args...)
	for i := range out {
		if i > 0 && (out[i-1] == "--password" || out[i-1] == "--code") {
			out[i] = "***"
		}
	}
	return out
}

// proc is a long-running remohop (or other) process with captured output.
type proc struct {
	name string
	cmd  *exec.Cmd
	mu   sync.Mutex
	buf  bytes.Buffer
	done chan struct{}
}

func (p *proc) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buf.Write(b)
}

func (p *proc) output() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.buf.String()
}

// waitFor waits until the output matches re and returns the first match's
// groups.
func (p *proc) waitFor(t *testing.T, re *regexp.Regexp, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if m := re.FindStringSubmatch(p.output()); m != nil {
			return m
		}
		select {
		case <-p.done:
			t.Fatalf("%s exited before printing %q:\n%s", p.name, re, clip(p.output()))
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not print %q within %s:\n%s", p.name, re, timeout, clip(p.output()))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func startProc(t *testing.T, name string, env []string, bin string, args ...string) *proc {
	t.Helper()
	p := &proc{name: name, done: make(chan struct{})}
	p.cmd = exec.Command(bin, args...)
	p.cmd.Env = env
	p.cmd.Stdout, p.cmd.Stderr = p, p
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", name, err)
	}
	go func() { p.cmd.Wait(); close(p.done) }()
	t.Cleanup(func() {
		p.cmd.Process.Signal(os.Interrupt)
		select {
		case <-p.done:
		case <-time.After(5 * time.Second):
			p.cmd.Process.Kill()
			<-p.done
		}
	})
	return p
}

// start runs a long-lived remohop command on the device.
func (d *device) start(args ...string) *proc {
	d.t.Helper()
	return startProc(d.t, d.name+": remohop "+args[0], d.environ(), remohopBin(d.t), args...)
}

// freePort returns a currently unused port on 127.0.0.1 (TCP and UDP).
func freePort(t *testing.T) int {
	t.Helper()
	for i := 0; i < 50; i++ {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		l.Close()
		if u, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(port)); err == nil {
			u.Close()
			if u2, err := net.ListenPacket("udp", "127.0.0.1:"+strconv.Itoa(port+1)); err == nil {
				u2.Close()
				return port
			}
		}
	}
	t.Fatal("no free port")
	return 0
}

// gate skips the test unless env is "1".
func gate(t *testing.T, env string) {
	t.Helper()
	if os.Getenv(env) != "1" {
		t.Skipf("set %s=1 to run this end-to-end test", env)
	}
}

// eventually polls f until it returns true.
func eventually(t *testing.T, what string, timeout time.Duration, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

var hostIDRE = regexp.MustCompile(`ID:\s+([0-9 ]{9,11})`)

func digits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}
