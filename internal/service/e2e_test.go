package service

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestServiceE2E installs a real service with the real service manager, so
// it only runs when asked to:
//
//	REMOHOP_SERVICE_E2E=1 go test -count=1 -v -run TestServiceE2E ./internal/service
//
// macOS: as a normal logged-in user (LaunchAgent). Windows: elevated.
// Linux: needs a systemd user manager. The instance gets a private config
// directory and --no-signal, and is always uninstalled again.
func TestServiceE2E(t *testing.T) {
	if os.Getenv("REMOHOP_SERVICE_E2E") != "1" {
		t.Skip("set REMOHOP_SERVICE_E2E=1 to install, run and remove a real service")
	}
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "remohop")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/readori/RemoHop/cmd/remohop").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	cfg := filepath.Join(tmp, "state")
	const name = "remohop-e2e"

	// remohop runs the built binary; hostEnv addresses the service's instance.
	remohop := func(hostEnv bool, args ...string) (string, error) {
		cmd := exec.Command(bin, args...)
		cmd.Env = os.Environ()
		if hostEnv {
			cmd.Env = append(cmd.Env, "REMOHOP_CONFIG_DIR="+cfg)
		}
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		err := cmd.Run()
		return out.String(), err
	}
	status := func() Status {
		out, _ := remohop(false, "service", "status", "--json", "--name", name) // exits 1 unless running
		var st Status
		if err := json.Unmarshal([]byte(out), &st); err != nil {
			t.Fatalf("service status --json: %v\n%s", err, out)
		}
		return st
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(45 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(500 * time.Millisecond)
		}
	}
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := remohop(false, "service", "logs", "-n", "200", "--name", name)
			t.Logf("service log:\n%s", out)
			out, _ = remohop(false, "service", "status", "--name", name)
			t.Logf("service status:\n%s", out)
		}
		out, err := remohop(false, "service", "uninstall", "--name", name)
		if err != nil {
			t.Errorf("cleanup uninstall: %v\n%s", err, out)
		}
	})

	install := []string{"service", "install", "--name", name, "--exe", bin, "--no-signal", "--config-dir", cfg}
	if runtime.GOOS != "windows" {
		install = append(install, "--user")
	}
	out, err := remohop(false, install...)
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	t.Logf("install:\n%s", out)

	waitFor("the service to run", func() bool { return status().Running })
	st := status()
	if !st.Installed || st.PID <= 0 {
		t.Errorf("status after install: %+v", st)
	}

	// The host inside the service must be up and using the private config
	// directory: `remohop status` reads the record it writes there.
	hostUp := func() bool {
		out, _ := remohop(true, "status", "--json")
		var hs struct {
			Running bool `json:"running"`
		}
		return json.Unmarshal([]byte(out), &hs) == nil && hs.Running
	}
	waitFor("`remohop status` to report the host", hostUp)

	if out, err := remohop(false, "service", "stop", "--name", name); err != nil {
		t.Fatalf("stop: %v\n%s", err, out)
	}
	waitFor("the service to stop", func() bool { return !status().Running })
	if st := status(); !st.Installed {
		t.Errorf("stop removed the service: %+v", st)
	}

	if out, err := remohop(false, "service", "uninstall", "--name", name); err != nil || !strings.Contains(out, "removed") {
		t.Fatalf("uninstall: %v\n%s", err, out)
	}
	if st := status(); st.Installed {
		t.Errorf("still installed after uninstall: %+v", st)
	}
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		plist := filepath.Join(home, "Library", "LaunchAgents", "com.remohop."+name+".plist")
		if _, err := os.Stat(plist); !os.IsNotExist(err) {
			t.Errorf("%s still exists (%v)", plist, err)
		}
	}
}
