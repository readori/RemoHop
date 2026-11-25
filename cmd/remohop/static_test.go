package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The remohop command must stay one static binary on Linux. The desktop
// app's native windows (internal/webview) load GTK through purego, which
// makes a binary dynamically linked, so neither may be linked here.
func TestStaticOnLinux(t *testing.T) {
	gobin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(gobin); err != nil {
		t.Skip("no go command:", err)
	}
	for _, arch := range []string{"amd64", "arm64"} {
		cmd := exec.Command(gobin, "list", "-deps", ".")
		cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("go list: %v", err)
		}
		for _, p := range strings.Fields(string(out)) {
			if p == "github.com/ebitengine/purego" || p == "github.com/readori/RemoHop/internal/webview" {
				t.Errorf("linux/%s: cmd/remohop depends on %s, which would make it a dynamic binary", arch, p)
			}
		}
	}
}
