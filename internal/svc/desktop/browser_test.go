package desktop

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/cli"
)

// The opener (and the browser it starts) must never see the viewer token on
// its command line; it gets a private redirect file instead.
func TestOpenBrowserHidesToken(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/true as the opener")
	}
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	var args []string
	old := browserCommand
	browserCommand = func(target string) *exec.Cmd {
		cmd := exec.Command("true", target)
		args = cmd.Args
		return cmd
	}
	defer func() { browserCommand = old }()

	const url = "http://127.0.0.1:8123/view/SECRETTOKENSECRETTOKEN?a=1&b=<x>"
	var stderr bytes.Buffer
	env := &cli.Env{Stderr: &stderr}
	cleanup := openBrowser(env, url)

	if len(args) != 2 {
		t.Fatalf("opener args %v (stderr %q)", args, stderr.String())
	}
	for _, a := range args {
		if strings.Contains(a, "SECRETTOKEN") {
			t.Fatalf("token on the opener's command line: %v", args)
		}
	}
	path := strings.TrimPrefix(args[1], "file://")
	if !filepath.IsAbs(path) || !strings.HasSuffix(path, ".html") {
		t.Fatalf("opener target %q", args[1])
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	if d, _ := os.Stat(filepath.Dir(path)); d.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", d.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	page := string(b)
	if !strings.Contains(page, "location.replace(") || !strings.Contains(page, `http-equiv="refresh"`) ||
		!strings.Contains(page, "SECRETTOKEN") || strings.Contains(page, "<x>") || strings.Contains(page, "src=") {
		t.Fatalf("page %s", page)
	}

	start := time.Now()
	cleanup()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file not removed: %v", err)
	}
	if time.Since(start) > minGrace+time.Second {
		t.Fatalf("cleanup took %v", time.Since(start))
	}
	if strings.Contains(stderr.String(), "SECRETTOKEN") {
		t.Fatal("token logged")
	}
}

func TestOpenBrowserMissingOpener(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	old := browserCommand
	browserCommand = func(string) *exec.Cmd { return exec.Command("/nonexistent/opener") }
	defer func() { browserCommand = old }()
	var stderr bytes.Buffer
	cleanup := openBrowser(&cli.Env{Stderr: &stderr}, "http://x/SECRET")
	cleanup()
	dir, _ := os.ReadDir(filepath.Join(os.Getenv("REMOHOP_CONFIG_DIR"), "run"))
	for _, e := range dir {
		if strings.HasPrefix(e.Name(), "open-") {
			t.Fatalf("redirect file left behind: %s", e.Name())
		}
	}
	if !strings.Contains(stderr.String(), "open the URL above manually") || strings.Contains(stderr.String(), "SECRET") {
		t.Fatalf("stderr %q", stderr.String())
	}
}
