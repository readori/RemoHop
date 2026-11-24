package mount

import (
	"testing"
)

// TestDavE2EWindows maps a free drive letter through the WebClient
// redirector and uses it through ordinary file calls. Run it with:
//
//	set REMOHOP_MOUNT_E2E=1
//	go test -count=1 -run TestDavE2EWindows -v ./internal/svc/fs/mount/
func TestDavE2EWindows(t *testing.T) {
	e2eEnabled(t)
	if err := webClientCheck(); err != nil {
		t.Skipf("the WebClient service is not usable on this machine: %v", err)
	}
	if !webClientRunning() {
		t.Skip("the WebClient service is installed but could not be started (it needs an elevated `net start WebClient` or the feature's trigger); `sc query WebClient` shows its state")
	}
	if lim := fileSizeLimit(); lim > 0 && lim < 21<<20 {
		t.Skipf("FileSizeLimitInBytes is %d: too small for the 20 MB round trip", lim)
	}
	d, remote := e2eMount(t, "*")
	e2eExercise(t, d.Target+`\`, remote)
}
