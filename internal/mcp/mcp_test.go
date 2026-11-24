package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/peer"
)

var wantTools = []string{
	"remohop_click", "remohop_clipboard_get", "remohop_clipboard_history", "remohop_clipboard_set",
	"remohop_displays", "remohop_download", "remohop_exec",
	"remohop_forward_list", "remohop_forward_start", "remohop_forward_stop",
	"remohop_info", "remohop_job_kill", "remohop_job_logs", "remohop_job_wait", "remohop_jobs", "remohop_key",
	"remohop_limits", "remohop_list_dir", "remohop_move", "remohop_peers", "remohop_read_file", "remohop_screenshot", "remohop_scroll",
	"remohop_send", "remohop_spawn", "remohop_type", "remohop_upload", "remohop_wake", "remohop_write_file",
}

func TestInitializeAndListTools(t *testing.T) {
	h := newHarness(t)
	init := h.cs.InitializeResult()
	if init.ServerInfo.Name != "remohop" || init.Instructions == "" || init.Capabilities.Tools == nil {
		t.Fatalf("unexpected initialize result: %+v", init)
	}
	res, err := h.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
		if len(tool.Description) < 40 {
			t.Errorf("%s: description too short: %q", tool.Name, tool.Description)
		}
		b, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Type       string         `json:"type"`
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(b, &schema); err != nil || schema.Type != "object" {
			t.Errorf("%s: input schema is not an object: %s", tool.Name, b)
		}
		if tool.Name != "remohop_clipboard_history" && tool.Name != "remohop_peers" && tool.Name != "remohop_forward_stop" && tool.Name != "remohop_forward_list" {
			if _, ok := schema.Properties["target"]; !ok {
				t.Errorf("%s: no target property", tool.Name)
			}
		}
		if _, ok := schema.Properties["password"]; ok {
			t.Errorf("%s: must not take a password", tool.Name)
		}
		if tool.Annotations == nil || tool.Annotations.Title == "" || tool.Annotations.OpenWorldHint == nil {
			t.Errorf("%s: incomplete annotations: %+v", tool.Name, tool.Annotations)
		}
		if tool.OutputSchema == nil {
			t.Errorf("%s: no output schema", tool.Name)
		}
		a := tool.Annotations
		if a != nil && !a.ReadOnlyHint && a.DestructiveHint == nil {
			t.Errorf("%s: write tool must state destructiveHint explicitly", tool.Name)
		}
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(wantTools, ",") {
		t.Fatalf("tool list changed:\n got %v\nwant %v", got, wantTools)
	}
	byName := map[string]*struct{ ro, destructive bool }{}
	for _, tool := range res.Tools {
		a := tool.Annotations
		byName[tool.Name] = &struct{ ro, destructive bool }{a.ReadOnlyHint, a.DestructiveHint != nil && *a.DestructiveHint}
	}
	for _, n := range []string{"remohop_info", "remohop_jobs", "remohop_screenshot", "remohop_read_file", "remohop_list_dir"} {
		if !byName[n].ro {
			t.Errorf("%s should be read-only", n)
		}
	}
	for _, n := range []string{"remohop_exec", "remohop_spawn", "remohop_job_kill", "remohop_write_file", "remohop_upload", "remohop_download", "remohop_clipboard_set", "remohop_click", "remohop_type", "remohop_key"} {
		if !byName[n].destructive {
			t.Errorf("%s should be marked destructive", n)
		}
	}
}

func TestPing(t *testing.T) {
	h := newHarness(t)
	if err := h.cs.Ping(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownToolAndBadArguments(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.cs.CallTool(ctx, &sdk.CallToolParams{Name: "remohop_nope"}); err == nil {
		t.Error("unknown tool: expected a protocol error")
	}
	// A wrongly typed argument must be rejected before anything runs.
	res, err := h.cs.CallTool(ctx, &sdk.CallToolParams{Name: "remohop_exec", Arguments: map[string]any{"command": 42}})
	if err == nil && !res.IsError {
		t.Error("bad argument type accepted")
	}
	res, err = h.cs.CallTool(ctx, &sdk.CallToolParams{Name: "remohop_exec", Arguments: map[string]any{}})
	if err == nil && !res.IsError {
		t.Error("missing command accepted")
	}
	if h.dials.Load() != 0 {
		t.Errorf("rejected calls dialled %d times", h.dials.Load())
	}
	if msg := h.fail("remohop_exec", map[string]any{"command": "  "}); !strings.Contains(msg, "empty") {
		t.Errorf("blank command: %s", msg)
	}
}

func TestExec(t *testing.T) {
	h := newHarness(t)
	var out execOut

	h.ok("remohop_exec", map[string]any{"command": sh.outAndErr("hello", "oops")}, &out)
	if out.ExitCode != 0 || out.Stdout != "hello\n" || out.Stderr != "oops\n" || out.TimedOut || out.DurationMS < 0 {
		t.Fatalf("success: %+v", out)
	}

	res := h.ok("remohop_exec", map[string]any{"command": sh.printExit("partial", 7)}, &out)
	if out.ExitCode != 7 || out.Stdout != "partial\n" {
		t.Fatalf("failure: %+v", out)
	}
	if !strings.Contains(text(res), "exit code 7") {
		t.Errorf("text should state the exit code: %q", text(res))
	}

	cwd := t.TempDir()
	h.ok("remohop_exec", map[string]any{"command": sh.stdinEnvCwd, "stdin": "from-stdin\n", "env": map[string]any{"FOO": "bar"}, "cwd": cwd}, &out)
	// The directory may print in resolved form (/private/var on macOS, long
	// names on Windows); its last element is unique enough.
	lines := strings.Split(strings.TrimSpace(out.Stdout), "\n")
	if len(lines) != 3 || lines[0] != "from-stdin" || lines[1] != "bar" || !strings.HasSuffix(lines[2], filepath.Base(cwd)) {
		t.Fatalf("stdin/env/cwd: %q", out.Stdout)
	}

	start := time.Now()
	res = h.ok("remohop_exec", map[string]any{"command": sh.printSleep("before", 30), "timeout_seconds": 1}, &out)
	if !out.TimedOut || out.Stdout != "before\n" || time.Since(start) > 15*time.Second {
		t.Fatalf("timeout: %+v after %s", out, time.Since(start))
	}
	if !strings.Contains(text(res), "TIMED OUT") {
		t.Errorf("text should flag the timeout: %q", text(res))
	}

	res = h.ok("remohop_exec", map[string]any{"command": sh.bigOutErr, "max_output_bytes": 100}, &out)
	if !out.StdoutTruncated || len(out.Stdout) != 100 || out.StdoutBytes < 5000 || out.StderrTruncated || out.Stderr != "err\n" {
		t.Fatalf("truncation: stdout=%d truncated=%v total=%d stderr=%q", len(out.Stdout), out.StdoutTruncated, out.StdoutBytes, out.Stderr)
	}
	if !strings.Contains(text(res), "stdout truncated") {
		t.Errorf("text should flag truncation: %q", text(res))
	}

	h.ok("remohop_exec", map[string]any{"command": sh.sleep(5), "timeout_seconds": 1}, &out)
	if !out.TimedOut {
		t.Errorf("killed command not reported as timed out: %+v", out)
	}
	// Windows has no signals; the kill shows only as the timeout there.
	if !onWindows && out.Signal == "" && out.ExitCode == 0 {
		t.Errorf("killed command reported as success: %+v", out)
	}
}

func TestExecCleansRemoteOutput(t *testing.T) {
	h := newHarness(t)
	var out execOut
	h.ok("remohop_exec", map[string]any{"command": sh.ansi}, &out)
	want := "red ok\nline\n"
	if onWindows {
		want = "red ok\n"
	}
	if out.Stdout != want {
		t.Fatalf("not sanitised: %q", out.Stdout)
	}
}

func TestExecErrors(t *testing.T) {
	h := newHarness(t)
	msg := h.fail("remohop_exec", map[string]any{"command": sh.noop, "cwd": filepath.Join(t.TempDir(), "not", "here")})
	if !strings.Contains(msg, "could not start") {
		t.Errorf("bad cwd: %s", msg)
	}
	if msg := h.fail("remohop_exec", map[string]any{"command": sh.noop, "env": map[string]any{"A=B": "x"}}); !strings.Contains(msg, "environment") {
		t.Errorf("bad env: %s", msg)
	}
}

func TestJobs(t *testing.T) {
	h := newHarness(t)
	var job jobInfo
	h.ok("remohop_spawn", map[string]any{"command": sh.twoSteps, "name": "worker"}, &job)
	if job.ID == "" || job.State != "running" || job.Name != "worker" {
		t.Fatalf("spawn: %+v", job)
	}

	var logs jobLogsOut
	// Following ends as soon as the job does, well before the 30 s window.
	start := time.Now()
	h.ok("remohop_job_logs", map[string]any{"job": job.ID, "wait_seconds": 30}, &logs)
	if logs.Output != "one\ntwo\n" || logs.Job.State != "exited" || logs.Job.ExitCode == nil || *logs.Job.ExitCode != 3 {
		t.Fatalf("logs: %+v", logs)
	}
	if time.Since(start) > 15*time.Second {
		t.Fatalf("following did not stop at job end")
	}

	var w jobWaitOut
	h.ok("remohop_job_wait", map[string]any{"job": "worker"}, &w)
	if w.TimedOut || w.Job.State != "exited" {
		t.Fatalf("wait on finished job: %+v", w)
	}

	var jobs jobsOut
	h.ok("remohop_jobs", nil, &jobs)
	if len(jobs.Jobs) != 1 || jobs.Jobs[0].ID != job.ID {
		t.Fatalf("jobs: %+v", jobs)
	}
	h.ok("remohop_jobs", map[string]any{"running_only": true}, &jobs)
	if len(jobs.Jobs) != 0 {
		t.Fatalf("running_only: %+v", jobs)
	}

	// tail_lines
	h.ok("remohop_job_logs", map[string]any{"job": job.ID, "tail_lines": 1}, &logs)
	if logs.Output != "two\n" {
		t.Fatalf("tail: %q", logs.Output)
	}

	// A long-running job: a short follow window returns while it still runs,
	// wait times out without blocking, and kill ends it.
	h.ok("remohop_spawn", map[string]any{"command": sh.printSleep("started", 60), "name": "sleeper"}, &job)
	start = time.Now()
	h.ok("remohop_job_logs", map[string]any{"job": "sleeper", "wait_seconds": 1}, &logs)
	if logs.Job.State != "running" || !strings.Contains(logs.Output, "started") || time.Since(start) > 10*time.Second {
		t.Fatalf("follow window: %+v after %s", logs, time.Since(start))
	}
	h.ok("remohop_job_wait", map[string]any{"job": "sleeper", "timeout_seconds": 1}, &w)
	if !w.TimedOut || w.Job.State != "running" {
		t.Fatalf("wait timeout: %+v", w)
	}
	h.ok("remohop_job_kill", map[string]any{"job": "sleeper", "signal": "KILL"}, &job)
	h.ok("remohop_job_wait", map[string]any{"job": "sleeper", "timeout_seconds": 10}, &w)
	if w.TimedOut || w.Job.State == "running" {
		t.Fatalf("after kill: %+v", w)
	}
	// 128+SIGKILL where there are signals; Windows reports whatever
	// TerminateProcess left behind.
	if !onWindows && (w.Job.State != "exited" || w.Job.ExitCode == nil || *w.Job.ExitCode != 137) {
		t.Fatalf("after kill: %+v", w)
	}

	if msg := h.fail("remohop_job_logs", map[string]any{"job": "nonesuch"}); !strings.Contains(msg, "nonesuch") {
		t.Errorf("unknown job: %s", msg)
	}
}

func TestJobLogsMaxBytesKeepsNewest(t *testing.T) {
	h := newHarness(t)
	var job jobInfo
	h.ok("remohop_spawn", map[string]any{"command": sh.count2000}, &job)
	var w jobWaitOut
	h.ok("remohop_job_wait", map[string]any{"job": job.ID}, &w)
	var logs jobLogsOut
	h.ok("remohop_job_logs", map[string]any{"job": job.ID, "tail_lines": 2000, "max_bytes": 50}, &logs)
	// 50 raw bytes; CRLF line ends (Windows) fold to LF afterwards.
	if !logs.Truncated || len(logs.Output) > 50 || len(logs.Output) < 40 || !strings.HasSuffix(logs.Output, "2000\n") {
		t.Fatalf("got truncated=%v %q", logs.Truncated, logs.Output)
	}
}

func TestSysinfoAndPeers(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	h := newHarness(t)
	res := h.ok("remohop_info", nil, nil)
	if !strings.Contains(text(res), "host:") || !strings.Contains(text(res), "display:") {
		t.Errorf("info text: %s", text(res))
	}
	var info map[string]any
	h.ok("remohop_info", nil, &info)
	// What every OS reports; the rest is filled in per platform.
	cores, _ := info["cpu_cores"].(float64)
	if info["os"] != runtime.GOOS || info["arch"] != runtime.GOARCH || info["hostname"] == "" || info["user"] == "" || cores < 1 {
		t.Errorf("info: %v", info)
	}
	if runtime.GOOS == "linux" {
		mem, _ := info["mem_total_bytes"].(float64)
		up, _ := info["uptime_seconds"].(float64)
		if mem <= 0 || up <= 0 || info["kernel"] == "" {
			t.Errorf("linux info: %v", info)
		}
	}
	if _, has := info["connection_path"]; has {
		t.Errorf("peertest sessions have no path: %v", info["connection_path"])
	}

	var peers peersOut
	h.ok("remohop_peers", nil, &peers)
	if len(peers.Peers) != 0 || peers.DefaultTarget != "123456789" {
		t.Fatalf("empty store: %+v", peers)
	}
	store, err := dial.OpenStore("")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update("987654321", func(p *dial.Peer) error {
		p.Alias = "buildbox"
		p.TokenID = "t"
		p.Token = []byte("secret")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	res = h.ok("remohop_peers", nil, &peers)
	if len(peers.Peers) != 1 || peers.Peers[0].Alias != "buildbox" || !peers.Peers[0].Paired || strings.Contains(text(res), "secret") {
		t.Fatalf("peers: %+v %s", peers, text(res))
	}
}

func TestPoolSharesOneDial(t *testing.T) {
	h := newHarness(t)
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := h.call("remohop_exec", map[string]any{"command": fmt.Sprintf("echo %d", i)})
			if res.IsError {
				t.Errorf("call %d: %s", i, text(res))
			}
		}()
	}
	wg.Wait()
	if n := h.dials.Load(); n != 1 {
		t.Fatalf("20 concurrent calls dialled %d times, want 1", n)
	}
	for range 5 {
		h.ok("remohop_info", nil, nil)
	}
	if n := h.dials.Load(); n != 1 {
		t.Fatalf("sequential calls re-dialled: %d", n)
	}
	// Different targets get different sessions.
	h.ok("remohop_info", map[string]any{"target": "987654321"}, nil)
	if n := h.dials.Load(); n != 2 {
		t.Fatalf("second target: %d dials", n)
	}
}

func TestPoolRedialsDeadSession(t *testing.T) {
	h := newHarness(t)
	h.ok("remohop_info", nil, nil)
	h.mu.Lock()
	h.pairs[0].Close()
	h.mu.Unlock()
	eventually(t, "session death", func() bool { return dead(h.pairs[0]) })
	h.ok("remohop_info", nil, nil)
	if n := h.dials.Load(); n != 2 {
		t.Fatalf("dials = %d, want 2", n)
	}
}

func TestPoolRetriesWhenSessionDiesBetweenChecks(t *testing.T) {
	h := newHarness(t)
	h.ok("remohop_info", nil, nil)
	// Kill the host side: the client side notices only when it is used.
	h.mu.Lock()
	h.hosts[0].Close()
	h.mu.Unlock()
	h.ok("remohop_info", nil, nil) // safe-to-repeat tool: transparently re-dialled
	if n := h.dials.Load(); n != 2 {
		t.Fatalf("dials = %d, want 2", n)
	}
}

func TestNonRepeatableToolReportsLostSession(t *testing.T) {
	h := newHarness(t)
	h.ok("remohop_exec", map[string]any{"command": sh.noop}, nil)
	h.mu.Lock()
	h.hosts[0].Close()
	h.mu.Unlock()
	res := h.call("remohop_exec", map[string]any{"command": "echo again"})
	if res.IsError {
		if !strings.Contains(text(res), "lost") {
			t.Errorf("error should say the session was lost: %s", text(res))
		}
	}
	// Whatever happened, the next call works on a fresh session.
	var out execOut
	h.ok("remohop_exec", map[string]any{"command": "echo fine"}, &out)
	if out.Stdout != "fine\n" {
		t.Fatalf("after loss: %+v", out)
	}
}

func TestPoolClosesIdleSessions(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.IdleTimeout = 100 * time.Millisecond })
	h.ok("remohop_info", nil, nil)
	eventually(t, "idle close", func() bool { return dead(h.pairs[0]) })
	h.ok("remohop_info", nil, nil)
	if n := h.dials.Load(); n != 2 {
		t.Fatalf("dials = %d, want 2", n)
	}
}

func TestShutdownClosesSessions(t *testing.T) {
	h := newHarness(t)
	h.ok("remohop_info", nil, nil)
	h.srv.Close()
	if !dead(h.pairs[0]) {
		t.Fatal("session survived shutdown")
	}
	res := h.call("remohop_info", nil)
	if !res.IsError {
		t.Fatal("call after shutdown succeeded")
	}
}

func TestDialFailures(t *testing.T) {
	t.Run("no credential", func(t *testing.T) {
		h := newHarness(t, func(o *Options) {
			o.Dial = func(context.Context, string) (*peer.Conn, error) {
				return nil, fmt.Errorf("dial: %w", dial.ErrNoCredential)
			}
		})
		msg := h.fail("remohop_exec", map[string]any{"command": "true", "target": "123 456 789"})
		if !strings.Contains(msg, "`remohop 123456789 pair`") || strings.Contains(msg, "password\":") {
			t.Errorf("pairing hint missing: %s", msg)
		}
	})
	t.Run("generic", func(t *testing.T) {
		h := newHarness(t, func(o *Options) {
			o.Dial = func(context.Context, string) (*peer.Conn, error) { return nil, errors.New("host offline") }
		})
		msg := h.fail("remohop_info", nil)
		if !strings.Contains(msg, "host offline") || !strings.Contains(msg, "remohop 123456789 pair") {
			t.Errorf("message: %s", msg)
		}
		// A failed dial is not cached.
		h.fail("remohop_info", nil)
	})
	t.Run("no target", func(t *testing.T) {
		h := newHarness(t, func(o *Options) { o.DefaultTarget = "" })
		msg := h.fail("remohop_info", nil)
		if !strings.Contains(msg, "no target") {
			t.Errorf("message: %s", msg)
		}
		h.ok("remohop_info", map[string]any{"target": "123456789"}, nil)
	})
	t.Run("host key changed", func(t *testing.T) {
		h := newHarness(t, func(o *Options) {
			o.Dial = func(context.Context, string) (*peer.Conn, error) { return nil, dial.ErrHostKeyChanged }
		})
		if msg := h.fail("remohop_info", nil); !strings.Contains(msg, "Do not work around") {
			t.Errorf("message: %s", msg)
		}
	})
	t.Run("dial timeout", func(t *testing.T) {
		h := newHarness(t, func(o *Options) {
			o.DialTimeout = 100 * time.Millisecond
			o.Dial = func(ctx context.Context, _ string) (*peer.Conn, error) { <-ctx.Done(); return nil, ctx.Err() }
		})
		if msg := h.fail("remohop_info", nil); !strings.Contains(msg, "timed out") {
			t.Errorf("message: %s", msg)
		}
	})
}
