package mount

import (
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

// davMount mounts the server with macOS's own WebDAV filesystem:
//
//	mount_webdav -S [-o rdonly] -v <volume name> <url> <dir>
//
// -S suppresses the authentication dialogs (there is nothing to ask: the
// URL is the credential), -v names the volume in Finder.
func davMount(url string, port int, secret, target string, o Options) (string, error) {
	args := []string{"-S"}
	if o.ReadOnly {
		args = append(args, "-o", "rdonly")
	}
	name := o.FsName
	if name == "" {
		name = "remohop " + filepath.Base(target)
	}
	name = strings.NewReplacer("/", "-", ":", "-").Replace(name)
	args = append(args, "-v", name, url, target)
	out, err := exec.Command("/sbin/mount_webdav", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("mount_webdav: %v: %s", err, strings.TrimSpace(string(out)))
	}
	if !isMounted(target) {
		return "", errors.New("mount_webdav reported success but nothing is mounted")
	}
	return target, nil
}

func davUnmount(target string, force bool) error {
	if !force {
		return unmountDir(target, false)
	}
	if out, err := exec.Command("/usr/sbin/diskutil", "unmount", "force", target).CombinedOutput(); err != nil {
		if uerr := unmountDir(target, true); uerr != nil {
			return fmt.Errorf("diskutil: %v: %s; umount -f: %v", err, strings.TrimSpace(string(out)), uerr)
		}
	}
	return nil
}
