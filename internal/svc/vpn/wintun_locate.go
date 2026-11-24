package vpn

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/readori/RemoHop/internal/config"
)

// On Windows the tunnel interface comes from Wintun, the WireGuard
// project's TUN driver, which is a signed DLL distributed separately
// (https://www.wintun.net). remohop neither ships nor downloads it. It loads
// exactly one build of it, identified by hash: a DLL found on PATH, in the
// download folder or next to remohop.exe is executed with Administrator
// rights, so "a file named wintun.dll" is not good enough. A file that
// matches is copied into a directory only this user can write, and loaded
// from there, so it cannot be swapped between the check and the load.
const (
	wintunVersion   = "0.14.1"
	wintunURL       = "https://www.wintun.net/builds/wintun-0.14.1.zip"
	wintunZipSHA256 = "07c256185d6ee3652e09fa55c0b673e2624b565e02c4b9091c79ca7d2f24ef51"
	wintunMaxSize   = 4 << 20
)

// wintunBuilds maps GOARCH to the directory in the Wintun zip and the
// SHA-256 of that build's wintun.dll.
var wintunBuilds = map[string]struct{ dir, sha256 string }{
	"amd64": {"amd64", "e5da8447dc2c320edc0fc52fa01885c103de8c118481f683643cacc3220dafce"},
	"arm64": {"arm64", "f7ba89005544be9d85231a9e0d5f23b2d15b3311667e2dad0debd344918a3f80"},
	"386":   {"x86", "d694fa46ab4cfebcb2632d094c7aa97278eef2f8052438621766d863ae98a931"},
	"arm":   {"arm", "daad267411ecdc70a0535e274d2c3e9da3d0084bdac7662cb8424dd4a031b4d9"},
}

// wintunSearchDirs lists where wintun.dll is looked for, in order: the
// directory named by REMOHOP_WINTUN_DIR, the directory of the remohop executable,
// the config directory, and the directories on PATH.
func wintunSearchDirs() []string {
	var dirs []string
	if d := os.Getenv("REMOHOP_WINTUN_DIR"); d != "" {
		dirs = append(dirs, d)
	}
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	if d, err := config.Dir(); err == nil {
		dirs = append(dirs, d)
	}
	return append(dirs, filepath.SplitList(os.Getenv("PATH"))...)
}

// locateWintun finds the expected Wintun build for goarch in dirs, copies
// it into private (unless an identical copy is there already) and returns
// the copy's path.
func locateWintun(dirs []string, private, goarch string) (string, error) {
	build, ok := wintunBuilds[goarch]
	if !ok {
		return "", fmt.Errorf("there is no Wintun build for %s", goarch)
	}
	want, _ := hex.DecodeString(build.sha256)
	target := filepath.Join(private, "wintun-"+wintunVersion+"-"+build.dir+".dll")
	if sum, err := hashFile(target); err == nil && bytes.Equal(sum, want) {
		return target, nil
	}
	var rejected []string
	seen := map[string]bool{}
	for _, d := range dirs {
		if d == "" {
			continue
		}
		p := filepath.Join(d, "wintun.dll")
		if seen[strings.ToLower(p)] {
			continue
		}
		seen[strings.ToLower(p)] = true
		data, err := readLimited(p)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		if !bytes.Equal(sum[:], want) {
			rejected = append(rejected, fmt.Sprintf("%s (SHA-256 %x)", p, sum))
			continue
		}
		if err := config.WriteFileAtomic(target, data, 0o600); err != nil {
			return "", fmt.Errorf("copying %s to %s: %w", p, private, err)
		}
		return target, nil
	}
	return "", wintunMissing(build.dir, rejected)
}

func wintunMissing(archDir string, rejected []string) error {
	where := "next to remohop.exe or into the remohop config directory"
	if exe, err := os.Executable(); err == nil {
		if cfg, err := config.Dir(); err == nil {
			where = fmt.Sprintf("next to remohop.exe (into %s) or into the remohop config directory (%s)", filepath.Dir(exe), cfg)
		}
	}
	msg := fmt.Sprintf("the VPN on Windows needs the Wintun driver (wintun.dll), which remohop does not include and never downloads by itself. "+
		"Download Wintun %s from https://www.wintun.net (%s; check that its SHA-256 is %s, for example with `certutil -hashfile wintun-%s.zip SHA256`), "+
		"and copy wintun\\bin\\%s\\wintun.dll from it %s",
		wintunVersion, wintunURL, wintunZipSHA256, wintunVersion, archDir, where)
	if len(rejected) > 0 {
		msg += fmt.Sprintf(". Found, but not the Wintun %s build remohop loads: %s", wintunVersion, strings.Join(rejected, "; "))
	}
	return errors.New(msg)
}

func hashFile(p string) ([]byte, error) {
	data, err := readLimited(p)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return sum[:], nil
}

func readLimited(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, wintunMaxSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > wintunMaxSize {
		return nil, errors.New("too large to be wintun.dll")
	}
	return data, nil
}
