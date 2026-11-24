package forward_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

// syncBuf is a bytes.Buffer safe for the command goroutine and the test.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) { s.mu.Lock(); defer s.mu.Unlock(); return s.b.Write(p) }
func (s *syncBuf) String() string              { s.mu.Lock(); defer s.mu.Unlock(); return s.b.String() }

func newEnv(client *peer.Conn) (*cli.Env, *syncBuf, *syncBuf) {
	var out, errb syncBuf
	return &cli.Env{
		Target: "test", Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb,
		DialFunc: func(context.Context, *cli.Env) (*peer.Conn, error) { return client, nil },
	}, &out, &errb
}

func runCmd(t *testing.T, env *cli.Env, ctx context.Context, args ...string) chan int {
	t.Helper()
	ch := make(chan int, 1)
	go func() { ch <- cli.Lookup("forward").RemoteRun(ctx, env, args) }()
	return ch
}

// The commands record themselves under the remohop state directory; keep the
// tests out of the real one.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "remohop-forward-test-")
	if err != nil {
		panic(err)
	}
	os.Setenv("REMOHOP_CONFIG_DIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func TestCLIUsageErrors(t *testing.T) {
	client, _ := peertest.Pair(t)
	for _, args := range [][]string{{"-b"}, {"--udp"}, {"--udp", "-R", "1:1"}, {"bogus"}, {"-R", "x"}, {"80:0"}, {"--nope"}} {
		env, _, _ := newEnv(client)
		if code := cli.Lookup("forward").RemoteRun(context.Background(), env, args); code != cli.ExitUsage {
			t.Errorf("args %v: exit %d, want %d", args, code, cli.ExitUsage)
		}
	}
}

func TestCLIForwardJSON(t *testing.T) {
	client, _ := peertest.Pair(t)
	echo := echoServer(t)
	_, ep, _ := net.SplitHostPort(echo)
	env, out, errb := newEnv(client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A flag after the spec, and one spec per direction.
	done := runCmd(t, env, ctx, "0:"+ep, "--json", "-R", "0:"+ep)

	var lines []listenLine
	waitFor(t, "two listeners reported", func() bool {
		lines = lines[:0]
		sc := bufio.NewScanner(strings.NewReader(out.String()))
		for sc.Scan() {
			var l listenLine
			if json.Unmarshal(sc.Bytes(), &l) != nil {
				return false
			}
			lines = append(lines, l)
		}
		return len(lines) == 2
	})
	if lines[0].Direction != "local" || lines[1].Direction != "reverse" || lines[0].Target != echo {
		t.Fatalf("lines %+v", lines)
	}
	for _, l := range lines {
		c, err := net.Dial("tcp", l.Listen)
		if err != nil {
			t.Fatal(err)
		}
		c.Write([]byte("ping"))
		c.(*net.TCPConn).CloseWrite()
		b, _ := io.ReadAll(c)
		c.Close()
		if string(b) != "ping" {
			t.Fatalf("%s: echo %q", l.Direction, b)
		}
	}
	cancel()
	select {
	case code := <-done:
		if code != cli.ExitOK {
			t.Fatalf("exit %d, stderr %q", code, errb.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("command did not exit on cancel")
	}
}

type listenLine struct {
	Direction string `json:"direction"`
	Listen    string `json:"listen"`
	Target    string `json:"target"`
}

func TestCLISessionDeathExitsNonZero(t *testing.T) {
	client, host := peertest.Pair(t)
	env, out, errb := newEnv(client)
	done := runCmd(t, env, context.Background(), "0:1")
	waitFor(t, "listening", func() bool { return strings.Contains(out.String(), "forwarding 127.0.0.1:") })
	host.Close()
	select {
	case code := <-done:
		if code != cli.ExitError || !strings.Contains(errb.String(), "session closed") {
			t.Fatalf("exit %d stderr %q", code, errb.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("command did not exit after the session died")
	}
}

func TestCLIBindFailure(t *testing.T) {
	client, _ := peertest.Pair(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	_, p, _ := net.SplitHostPort(ln.Addr().String())
	env, _, errb := newEnv(client)
	if code := cli.Lookup("forward").RemoteRun(context.Background(), env, []string{p + ":1"}); code != cli.ExitError {
		t.Fatalf("exit %d (%s)", code, errb.String())
	}
}
