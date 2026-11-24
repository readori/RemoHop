package mcp

import (
	"context"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/peer"
)

// grantedHarness runs a real host daemon on loopback, issues a grant on it,
// redeems the code from a separate client identity and connects an MCP
// server that dials the host the way `remohop mcp` does. It returns the
// harness, the host's config directory and the host itself.
func grantedHarness(t *testing.T, req hostd.GrantRequest) (*harness, string, *hostd.Host) {
	t.Helper()
	hostDir := t.TempDir()
	noLocals := func() []netip.Addr { return nil }
	h, err := hostd.New(hostd.Config{Dir: hostDir, Listen: "127.0.0.1:0", Locals: noLocals})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	_, code, err := hostd.IssueGrant(hostDir, req)
	if err != nil {
		t.Fatal(err)
	}
	clientDir := t.TempDir()
	store, err := dial.OpenStore(clientDir)
	if err != nil {
		t.Fatal(err)
	}
	target := h.Addr().String()
	if err := dial.Redeem(store, target, code, ""); err != nil {
		t.Fatal(err)
	}
	hs := newHarness(t, func(o *Options) {
		o.DefaultTarget = target
		o.Dial = func(ctx context.Context, target string) (*peer.Conn, error) {
			return dial.Dial(ctx, target, dial.Options{Dir: clientDir, Locals: noLocals})
		}
	})
	return hs, hostDir, h
}

func TestGrantEndToEnd(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "notes.txt"), []byte("project notes"), 0o644)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("nope"), 0o644)
	hs, hostDir, _ := grantedHarness(t, hostd.GrantRequest{Name: "claude-session", For: time.Hour,
		Allow: []string{"shell:run-only", "files:read:" + root}, Commands: []string{"echo"}})

	var lim limitsOut
	res := hs.ok("remohop_limits", nil, &lim)
	if lim.Grant != "claude-session" || lim.Shell != "run-only" || len(lim.ReadRoots) != 1 || lim.ReadRoots[0] != root ||
		len(lim.Commands) != 1 || lim.Expires == "" || lim.WriteAll {
		t.Fatalf("limits: %+v", lim)
	}
	if txt := text(res); !strings.Contains(txt, "Access through grant claude-session") || !strings.Contains(txt, "Not allowed:") {
		t.Fatalf("limits text: %s", txt)
	}

	if res := hs.ok("remohop_exec", map[string]any{"command": "echo from-agent"}, nil); !strings.Contains(text(res), "from-agent") {
		t.Fatalf("allowed command: %s", text(res))
	}
	for _, c := range []struct {
		tool string
		args map[string]any
		want string
	}{
		{"remohop_exec", map[string]any{"command": "whoami"}, `command "whoami" is not allowed by grant claude-session`},
		{"remohop_exec", map[string]any{"command": "echo hi", "env": map[string]any{"A": "b"}}, "setting environment variables is not allowed by grant claude-session"},
		{"remohop_read_file", map[string]any{"path": filepath.Join(outside, "secret.txt")}, "files:read is not allowed by grant claude-session"},
		{"remohop_write_file", map[string]any{"path": filepath.Join(root, "new.txt"), "content": "x"}, "files:write is not allowed by grant claude-session"},
		{"remohop_screenshot", nil, "screenshot is not allowed by grant claude-session"},
		{"remohop_clipboard_get", nil, "clip is not allowed by grant claude-session"},
	} {
		msg := hs.fail(c.tool, c.args)
		if !strings.Contains(msg, c.want) || !strings.Contains(msg, "Do not try to work around it") || !strings.Contains(msg, "remohop_limits") {
			t.Errorf("%s %v: refusal %q lacks %q or the hint", c.tool, c.args, msg, c.want)
		}
	}
	if res := hs.ok("remohop_read_file", map[string]any{"path": filepath.Join(root, "notes.txt")}, nil); !strings.Contains(text(res), "project notes") {
		t.Fatalf("read in root: %s", text(res))
	}

	// Every action is in the host's audit log under the grant's name.
	entries, _ := hostd.GrantAudit(hostDir, "claude-session", 0)
	var acts []string
	for _, e := range entries {
		acts = append(acts, e.Event+":"+e.Fields["command"]+e.Fields["path"])
	}
	all := strings.Join(acts, ",")
	for _, want := range []string{"grant-action:echo from-agent", "grant-refused:whoami", "grant-refused:" + filepath.Join(outside, "secret.txt"), "grant-action:" + filepath.Join(root, "notes.txt")} {
		if !strings.Contains(all, want) {
			t.Errorf("audit lacks %q: %s", want, all)
		}
	}

	// Revoked mid-session: the session ends, and the agent is told the
	// grant is gone rather than to retry.
	if _, err := hostd.RevokeGrant(hostDir, "claude-session"); err != nil {
		t.Fatal(err)
	}
	msg := hs.fail("remohop_exec", map[string]any{"command": "echo after"})
	if !strings.Contains(msg, "revoked") && !strings.Contains(msg, "do not retry") {
		t.Fatalf("after revocation: %s", msg)
	}
}

func TestGrantApprovalThroughMCP(t *testing.T) {
	hs, hostDir, h := grantedHarness(t, hostd.GrantRequest{Name: "reviewed", For: time.Hour,
		Allow: []string{"shell:run-only"}, Approve: grant.ApproveEach})
	_ = hostDir
	go func() {
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if p := h.Pending(); len(p) > 0 {
				h.Decide(p[0].N, false)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	msg := hs.fail("remohop_exec", map[string]any{"command": "echo please"})
	if !strings.Contains(msg, "the host's owner denied: exec: echo please") || !strings.Contains(msg, "Do not repeat the action") {
		t.Fatalf("denied: %s", msg)
	}
}

func TestReadOnlyMode(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.ReadOnly = true })
	if !strings.Contains(h.cs.InitializeResult().Instructions, "read-only mode") {
		t.Error("instructions do not mention read-only mode")
	}
	res, err := h.cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
		if !tool.Annotations.ReadOnlyHint {
			t.Errorf("read-only mode offers %s", tool.Name)
		}
	}
	for _, want := range []string{"remohop_limits", "remohop_read_file", "remohop_list_dir", "remohop_screenshot", "remohop_info"} {
		if !names[want] {
			t.Errorf("read-only mode lacks %s", want)
		}
	}
	for _, gone := range []string{"remohop_exec", "remohop_write_file", "remohop_click", "remohop_forward_start", "remohop_clipboard_set", "remohop_spawn"} {
		if names[gone] {
			t.Errorf("read-only mode offers %s", gone)
		}
	}
}

func TestGrantHint(t *testing.T) {
	for in, want := range map[string]string{
		"files:write is not allowed by grant x":           "work around",
		"the host's owner denied: exec: ls (grant x ...)": "Do not repeat",
		"grant x expired at 2026-01-01T00:00:00Z":         "has ended",
		"no such file or directory":                       "",
	} {
		got := grantHint(in)
		if want == "" && got != in || want != "" && !strings.Contains(got, want) {
			t.Errorf("grantHint(%q) = %q", in, got)
		}
	}
}
