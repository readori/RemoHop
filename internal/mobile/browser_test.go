package mobile

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestPhoneBrowser runs mobile/shared/test/phone_test.mjs: headless Chrome
// at a phone viewport with touch emulation loads the in-process gateway
// with the apps' injected script and a fake native bridge, connects to a
// real host with the pairing PairWithPassword saved, and uses the terminal,
// files (including the app's download hand-over) and the desktop tab.
//
// It needs node (22+) and Chrome, so it runs only with
// REMOHOP_BROWSER_TEST=1; REMOHOP_SHOTS=/dir keeps screenshots.
func TestPhoneBrowser(t *testing.T) {
	if os.Getenv("REMOHOP_BROWSER_TEST") != "1" {
		t.Skip("set REMOHOP_BROWSER_TEST=1 (needs node and google-chrome)")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	initDir(t)
	SetDeviceName("Browser Test Phone")
	addr := startHost(t)
	if r := pairJSON(t, addr, hostPassword, "lab"); !r.OK {
		t.Fatalf("pair: %+v", r)
	}
	files := t.TempDir()
	if err := os.WriteFile(filepath.Join(files, "hello.txt"), []byte("hello from the host\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A second host, for the connect form's own pairing (address, password, pair box).
	formAddr := startHost(t)
	u, err := StartGateway()
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "mobile", "shared", "test", "phone_test.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, script, "--gateway")
	// The URL goes through the environment, not argv, which other local
	// users can read.
	cmd.Env = append(os.Environ(), "REMOHOP_GATEWAY_URL="+u, "REMOHOP_TARGET=lab", "REMOHOP_TEST_DIR="+files,
		"REMOHOP_HOST_ADDR="+formAddr, "REMOHOP_HOST_PASSWORD="+hostPassword)
	done := make(chan struct{})
	var out []byte
	go func() { out, err = cmd.CombinedOutput(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Minute):
		cmd.Process.Kill()
		<-done
		t.Fatalf("phone_test.mjs timed out:\n%s", out)
	}
	t.Logf("\n%s", out)
	if err != nil {
		t.Fatalf("phone_test.mjs: %v", err)
	}
}
