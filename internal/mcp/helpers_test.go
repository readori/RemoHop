package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

// harness is an MCP server wired to loopback hosts through the dial seam,
// with an SDK client connected over an in-memory transport.
type harness struct {
	t     *testing.T
	srv   *Server
	cs    *sdk.ClientSession
	dials atomic.Int32

	// authorize, if set before the first call, is the host's per-service
	// permission check (how a host owner's feature switches reach a session).
	authorize func(service string) error

	mu    sync.Mutex
	pairs []*peer.Conn // client halves, in dial order
	hosts []*peer.Conn
}

func newHarness(t *testing.T, tweak ...func(*Options)) *harness {
	t.Helper()
	return newHarnessWith(t, peer.HostServices, tweak...)
}

// newHarnessWith serves the given host-side registry instead of the real
// services, to simulate hosts whose behaviour the machine running the tests
// cannot provide (a headless one, for example).
func newHarnessWith(t *testing.T, services *peer.Registry, tweak ...func(*Options)) *harness {
	t.Helper()
	if os.Getenv("REMOHOP_CONFIG_DIR") == "" {
		// Job logs, clipboard history and saved peers all live here.
		t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	}
	h := &harness{t: t}
	opts := Options{
		DefaultTarget: "123456789",
		IdleTimeout:   -1,
		Dial: func(ctx context.Context, target string) (*peer.Conn, error) {
			h.dials.Add(1)
			// Dial takes a moment so concurrent callers really overlap.
			time.Sleep(20 * time.Millisecond)
			client, host := peertest.PairOpts(t, services, peertest.Options{Authorize: h.authorize})
			h.mu.Lock()
			h.pairs = append(h.pairs, client)
			h.hosts = append(h.hosts, host)
			h.mu.Unlock()
			return client, nil
		},
	}
	for _, f := range tweak {
		f(&opts)
	}
	h.srv = New(opts)
	h.connect()
	t.Cleanup(func() {
		h.cs.Close()
		h.srv.Close()
	})
	return h
}

func (h *harness) connect() {
	h.t.Helper()
	ctx := context.Background()
	ct, st := sdk.NewInMemoryTransports()
	if _, err := h.srv.Protocol().Connect(ctx, st, nil); err != nil {
		h.t.Fatal(err)
	}
	cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		h.t.Fatal(err)
	}
	h.cs = cs
}

// call invokes a tool and returns the raw result (which may have IsError).
func (h *harness) call(name string, args map[string]any) *sdk.CallToolResult {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := h.cs.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		h.t.Fatalf("%s: protocol error: %v", name, err)
	}
	return res
}

// ok calls a tool, requires success and decodes its structured output.
func (h *harness) ok(name string, args map[string]any, out any) *sdk.CallToolResult {
	h.t.Helper()
	res := h.call(name, args)
	if res.IsError {
		h.t.Fatalf("%s failed: %s", name, text(res))
	}
	if out != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			h.t.Fatal(err)
		}
		if err := json.Unmarshal(b, out); err != nil {
			h.t.Fatalf("%s: decoding structured content %s: %v", name, b, err)
		}
	}
	return res
}

// fail calls a tool, requires an isError result and returns its text.
func (h *harness) fail(name string, args map[string]any) string {
	h.t.Helper()
	res := h.call(name, args)
	if !res.IsError {
		h.t.Fatalf("%s unexpectedly succeeded: %s", name, text(res))
	}
	return text(res)
}

func text(res *sdk.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The remote shell is the host's own: POSIX sh on Linux and macOS, PowerShell
// on Windows. Tests run host and client in one process, so the commands they
// send have to be written for the machine running them.
type shellCmds struct {
	outAndErr   func(out, err string) string // print out on stdout and err on stderr
	printExit   func(out string, code int) string
	stdinEnvCwd string // echo stdin, then $FOO, then the working directory
	printSleep  func(out string, seconds float64) string
	sleep       func(seconds float64) string
	bigOutErr   string // 5000 "x" on stdout, then "err" on stderr
	ansi        string // "red" in colour, then " ok"
	twoSteps    string // "one", pause, "two", exit 3
	count2000   string // the numbers 1..2000, one per line
	noop        string
}

var sh = func() shellCmds {
	if runtime.GOOS == "windows" {
		return shellCmds{
			outAndErr: func(out, err string) string {
				return fmt.Sprintf("Write-Output '%s'; [Console]::Error.WriteLine('%s')", out, err)
			},
			printExit:   func(out string, code int) string { return fmt.Sprintf("Write-Output '%s'; exit %d", out, code) },
			stdinEnvCwd: "$input | ForEach-Object { Write-Output $_ }; Write-Output $env:FOO; Write-Output (Get-Location).Path",
			printSleep: func(out string, s float64) string {
				return fmt.Sprintf("[Console]::Out.WriteLine('%s'); [Console]::Out.Flush(); Start-Sleep -Milliseconds %d", out, int(s*1000))
			},
			sleep:     func(s float64) string { return fmt.Sprintf("Start-Sleep -Milliseconds %d", int(s*1000)) },
			bigOutErr: "Write-Output ('x' * 5000); [Console]::Error.WriteLine('err')",
			ansi:      "Write-Output \"$([char]27)[31mred$([char]27)[0m ok\"",
			twoSteps:  "[Console]::Out.WriteLine('one'); Start-Sleep -Milliseconds 300; [Console]::Out.WriteLine('two'); exit 3",
			count2000: "1..2000",
			noop:      "Write-Output ok",
		}
	}
	return shellCmds{
		outAndErr:   func(out, err string) string { return fmt.Sprintf("echo %s; echo %s >&2", out, err) },
		printExit:   func(out string, code int) string { return fmt.Sprintf("echo %s; exit %d", out, code) },
		stdinEnvCwd: "cat; echo $FOO; pwd",
		printSleep:  func(out string, s float64) string { return fmt.Sprintf("echo %s; sleep %g", out, s) },
		sleep:       func(s float64) string { return fmt.Sprintf("sleep %g", s) },
		bigOutErr:   "head -c 5000 /dev/zero | tr '\\0' x; echo; echo err >&2",
		ansi:        `printf '\033[31mred\033[0m \033]0;title\007ok\r\nline\001\n'`,
		twoSteps:    "echo one; sleep 0.3; echo two; exit 3",
		count2000:   "seq 1 2000",
		noop:        "true",
	}
}()

var onWindows = runtime.GOOS == "windows"
