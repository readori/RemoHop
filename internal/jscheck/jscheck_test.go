// Package jscheck only holds a test: every JavaScript file under internal/
// (the pages the desktop app, quick support, the web gateway and the
// remote-desktop viewer serve, and their test scripts) must parse. A
// syntax error leaves a page blank, and the browser tests that would notice
// run only on request.
package jscheck

import (
	"io/fs"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestScriptsParse(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	root := filepath.Join("..")
	n := 0
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) && p != root {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".js") {
			return nil
		}
		n++
		if out, err := exec.Command(node, "--check", p).CombinedOutput(); err != nil {
			t.Errorf("%s does not parse:\n%s", p, out)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n < 5 {
		t.Fatalf("found only %d scripts under internal/", n)
	}
}
