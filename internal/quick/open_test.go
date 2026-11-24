package quick

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Each way of opening the window gets its own redirect file, the browser
// sees only the file (never the token), and cleanup removes every file.
func TestShowWindowFilePerOpener(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses true as the opener")
	}
	dir := filepath.Join(t.TempDir(), "handoff")
	t.Setenv("REMOHOP_HANDOFF_DIR", dir)
	var seenFiles []string
	way := func(name string) opener {
		return opener{name: name, cmd: func(file string) *exec.Cmd {
			seenFiles = append(seenFiles, file)
			return exec.Command("true", file)
		}}
	}
	const target = "http://127.0.0.1:9/SECRETTOKEN/"
	calls := 0
	seen := func() bool { calls++; return len(seenFiles) == 2 } // the second opener "works"
	cleanup, err := showWindow(target, "Opening…", []opener{way("first"), way("second")}, 300*time.Millisecond, seen, func(string, ...any) {})
	if err != nil {
		t.Fatal(err)
	}
	if len(seenFiles) != 2 || seenFiles[0] == seenFiles[1] {
		t.Fatalf("redirect files %v", seenFiles)
	}
	for _, f := range seenFiles {
		if strings.Contains(f, "SECRETTOKEN") || filepath.Dir(f) != dir {
			t.Fatalf("redirect file %q", f)
		}
		b, err := os.ReadFile(f)
		if err != nil || !strings.Contains(string(b), "SECRETTOKEN") {
			t.Fatalf("redirect page %q: %v", b, err)
		}
	}
	cleanup()
	for _, f := range seenFiles {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Fatalf("%s not removed", f)
		}
	}
}
