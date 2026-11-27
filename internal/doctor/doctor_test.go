package doctor

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/signal"
	"github.com/readori/RemoHop/internal/tailnet"
)

func joined(advice []string) string { return strings.Join(advice, "\n") }

// TestAdvice: the common failures each get their own explanation.
func TestAdvice(t *testing.T) {
	base := func() *Report { return &Report{Tailscale: &tailnet.Status{}} }

	r := base()
	if a := joined(advise(r)); !strings.Contains(a, "remohop config set signal "+signal.OfficialAddr) || !strings.Contains(a, "HOST:PORT <command>") {
		t.Errorf("no server: %s", a)
	}

	r = base()
	r.Signal = SignalReport{Server: "rv.example.net", Pinned: true, Checked: true, PinMismatch: true}
	if a := joined(advise(r)); !strings.Contains(a, "signal-pin") || strings.Contains(a, "does not answer") {
		t.Errorf("pin mismatch: %s", a)
	}

	r = base()
	r.Signal = SignalReport{Server: "rv.example.net", Pinned: true, Checked: true}
	a := joined(advise(r))
	for _, want := range []string{"address and port", "`remohop signal` is running", "UDP 21116-21117", "websocket bridge as the last resort"} {
		if !strings.Contains(a, want) {
			t.Errorf("unreachable: %q missing in %s", want, a)
		}
	}
	if strings.Index(a, "websocket") < strings.Index(a, "UDP 21116-21117") {
		t.Errorf("unreachable: the websocket bridge should come last: %s", a)
	}
	r.Signal.Server = "rv.example.net:443"
	if a := joined(advise(r)); !strings.Contains(a, "UDP 443-444") {
		t.Errorf("unreachable on another port: %s", a)
	}

	r = base()
	r.Signal = SignalReport{Server: "rv.example.net", Pinned: true}
	r.Screen.FFmpegError = "ffmpeg not found on the host: screen sharing needs ffmpeg."
	if a := joined(advise(r)); !strings.Contains(a, "cannot see this machine's screen live") || !strings.Contains(a, "screenshot") {
		t.Errorf("no ffmpeg: %s", a)
	}
}

// TestDoctorTarget: `remohop doctor TARGET` (and `remohop TARGET doctor`)
// looks the machine up on the rendezvous server without logging in.
func TestDoctorTarget(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	t.Setenv("REMOHOP_TAILSCALE", "off")
	main, _ := net.ListenPacket("udp", "127.0.0.1:0")
	alt, _ := net.ListenPacket("udp", "127.0.0.1:0")
	srv, err := signal.NewServer(signal.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, main, alt) }()
	defer func() { cancel(); <-done; srv.Close() }()
	addr := main.LocalAddr().String()

	run := func(target string, args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		env := &cli.Env{Target: target, Stdout: &out, Stderr: &errb, Globals: cli.Globals{Signal: addr}}
		code := run(context.Background(), env, args)
		return code, out.String(), errb.String()
	}
	code, out, errOut := run("", "123456789")
	if code != cli.ExitOK || !strings.Contains(out, "Target 123456789") || !strings.Contains(out, "no host has this ID") ||
		!strings.Contains(out, "Check the ID with the person") {
		t.Fatalf("unknown ID: %d %s %s", code, out, errOut)
	}
	if code, out, _ := run("10.0.0.1:21118", "--offline"); code != cli.ExitOK || !strings.Contains(out, "no rendezvous server involved") {
		t.Fatalf("address as the target: %d %s", code, out)
	}
	if code, _, errOut := run("", "a", "b"); code != cli.ExitUsage || !strings.Contains(errOut, "see `remohop help doctor`") {
		t.Fatalf("two targets: %d %s", code, errOut)
	}
}
