package mount

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// davMount mounts the server with davfs2 (mount.davfs), the WebDAV client
// Linux distributions package. It exists mainly so the WebDAV path can be
// exercised on Linux, where FUSE is the default and the better choice:
// davfs2 needs root (or an fstab entry) and uploads files only when they
// are closed.
func davMount(url string, port int, secret, target string, o Options) (string, error) {
	if _, err := exec.LookPath("mount.davfs"); err != nil {
		return "", errors.New("--webdav needs davfs2 (mount.davfs) on Linux: install it, or use `-o nomount` and point any WebDAV client at the printed URL, or drop --webdav to use FUSE")
	}
	opts := fmt.Sprintf("uid=%d,gid=%d,nosuid,nodev", os.Getuid(), os.Getgid())
	if o.ReadOnly {
		opts += ",ro"
	}
	cmd := exec.Command("mount", "-t", "davfs", "-o", opts, url, target)
	cmd.Stdin = strings.NewReader("\n\n") // its username and password prompts
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("mount.davfs: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if !isMounted(target) {
		return "", errors.New("mount.davfs reported success but nothing is mounted")
	}
	return target, nil
}

func davUnmount(target string, force bool) error {
	if !force {
		// umount(8), not umount(2): mount.davfs's helper flushes its
		// cache when asked through its umount helper.
		out, err := exec.Command("umount", target).CombinedOutput()
		if err != nil {
			return fmt.Errorf("umount: %v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	return unmountDir(target, true)
}
