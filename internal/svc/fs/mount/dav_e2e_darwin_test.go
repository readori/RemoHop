package mount

import (
	"os"
	"testing"
)

// TestDavE2EDarwin mounts with macOS's mount_webdav and uses the mount
// through ordinary file calls. Run it with:
//
//	REMOHOP_MOUNT_E2E=1 go test -count=1 -run TestDavE2EDarwin -v ./internal/svc/fs/mount/
func TestDavE2EDarwin(t *testing.T) {
	e2eEnabled(t)
	if _, err := os.Stat("/sbin/mount_webdav"); err != nil {
		t.Skipf("no /sbin/mount_webdav: %v", err)
	}
	d, remote := e2eMount(t, t.TempDir())
	e2eExercise(t, d.Target, remote)
}
