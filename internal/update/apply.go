package update

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// One-click upgrades of a copy that came from the website (KindDownload):
// download the release's archive for this program, check it against the
// signed SHA256SUMS, put the new program next to the old one and swap them.
// The caller then starts the new program and quits.
//
// Copies installed by a package manager are left to it (Method.Command),
// and so is anything this cannot replace (a directory the user cannot
// write, quick support, an unknown system).

// Plan is what an upgrade of this program replaces and with which file.
type Plan struct {
	// Asset is the release file to download.
	Asset string `json:"asset"`
	// Member is the file (or, for an app bundle, the directory) inside it.
	Member string `json:"-"`
	// Target is what is replaced: the program, or on macOS the .app.
	Target string `json:"-"`
	// Bundle: Target is a macOS .app directory.
	Bundle bool `json:"-"`
}

// ErrUnsupported: this copy cannot upgrade itself.
var ErrUnsupported = errors.New("this copy cannot upgrade itself")

// PlanFor works out the upgrade of the program at exe (symbolic links
// resolved) on goos/goarch.
func PlanFor(goos, goarch, exe string) (Plan, error) {
	if goarch != "amd64" && goarch != "arm64" {
		return Plan{}, ErrUnsupported
	}
	// The program's name, whichever separator the path uses (tests plan
	// for Windows paths on other systems).
	base := path.Base(strings.ReplaceAll(exe, `\`, "/"))
	low := strings.ToLower(base)
	if strings.Contains(low, "quick") {
		// Quick support is downloaded fresh for each use.
		return Plan{}, ErrUnsupported
	}
	switch goos {
	case "windows":
		// The command-line program is remohop.exe (its archive, Scoop,
		// winget, install.ps1); the app is RemoHop.exe, or whatever the
		// user renamed it to. Names keep their case on disk.
		if base == "remohop.exe" {
			return Plan{Asset: "remohop_windows_" + goarch + ".zip", Member: "remohop.exe", Target: exe}, nil
		}
		return Plan{Asset: "RemoHop-windows-" + goarch + ".zip", Member: "RemoHop.exe", Target: exe}, nil
	case "darwin":
		if i := strings.Index(exe, ".app/Contents/MacOS/"); i >= 0 {
			bundle := exe[:i+len(".app")]
			return Plan{Asset: "RemoHop-macos.zip", Member: "RemoHop.app", Target: bundle, Bundle: true}, nil
		}
		return Plan{Asset: "remohop_darwin_universal.tar.gz", Member: "remohop", Target: exe}, nil
	case "linux":
		switch base {
		case "remohop-app":
			return Plan{Asset: "remohop-app_linux_" + goarch + ".tar.gz", Member: "remohop-app", Target: exe}, nil
		case "remohop":
			return Plan{Asset: "remohop_linux_" + goarch + ".tar.gz", Member: "remohop", Target: exe}, nil
		}
	}
	return Plan{}, ErrUnsupported
}

// Writable reports whether the user can replace p.Target: a file can be
// created next to it.
func (p Plan) Writable() bool {
	f, err := os.CreateTemp(filepath.Dir(p.Target), ".remohop-update-check-*")
	if err != nil {
		return false
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return true
}

// Limits on what is downloaded.
const (
	maxSums  = 64 << 10
	maxAsset = 300 << 20
)

// Progress reports a stage ("verifying", "downloading", "installing") and,
// while downloading, the percentage done (-1 when unknown).
type Progress func(stage string, percent int)

// Fetcher downloads release files; tests replace it.
type Fetcher struct {
	Client *http.Client
	// Base is the release download address with %s for the tag;
	// empty means GitHub's.
	Base string
}

func (f Fetcher) url(tag, name string) string {
	base := f.Base
	if base == "" {
		base = "https://github.com/" + Repo + "/releases/download/%s"
	}
	return fmt.Sprintf(base, tag) + "/" + name
}

func (f Fetcher) get(ctx context.Context, url string) (*http.Response, error) {
	c := f.Client
	if c == nil {
		c = &http.Client{Timeout: 15 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "RemoHop")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %d", path.Base(url), resp.StatusCode)
	}
	return resp, nil
}

func (f Fetcher) small(ctx context.Context, url string) ([]byte, error) {
	resp, err := f.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxSums+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSums {
		return nil, fmt.Errorf("%s: too large", path.Base(url))
	}
	return b, nil
}

// Download fetches and checks tag's archive for p and unpacks the new
// program next to p.Target. It returns the staged path, which Install puts
// in place. Nothing is changed when it fails.
func Download(ctx context.Context, f Fetcher, keys []ed25519.PublicKey, tag string, p Plan, progress Progress) (string, error) {
	if progress == nil {
		progress = func(string, int) {}
	}
	if v, ok := ParseVersion(tag); !ok || v.Pre != "" {
		return "", fmt.Errorf("%q is not a release version", tag)
	}
	progress("verifying", -1)
	sums, err := f.small(ctx, f.url(tag, SumsFile))
	if err != nil {
		return "", err
	}
	sig, err := f.small(ctx, f.url(tag, SigFile))
	if err != nil {
		return "", err
	}
	if err := Verify(keys, tag, sums, sig); err != nil {
		return "", err
	}
	table, err := ParseSums(sums)
	if err != nil {
		return "", err
	}
	want, ok := table[p.Asset]
	if !ok {
		return "", fmt.Errorf("%s is not in the release", p.Asset)
	}

	dir := filepath.Dir(p.Target)
	tmp, err := os.CreateTemp(dir, ".remohop-update-*.download")
	if err != nil {
		return "", err
	}
	defer func() { tmp.Close(); os.Remove(tmp.Name()) }()
	resp, err := f.get(ctx, f.url(tag, p.Asset))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	h := sha256.New()
	total := resp.ContentLength
	var done int64
	buf := make([]byte, 256<<10)
	last := -2
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			done += int64(n)
			if done > maxAsset {
				return "", fmt.Errorf("%s: too large", p.Asset)
			}
			h.Write(buf[:n])
			if _, err := tmp.Write(buf[:n]); err != nil {
				return "", err
			}
			pct := -1
			if total > 0 {
				pct = int(done * 100 / total)
			}
			if pct != last {
				progress("downloading", pct)
				last = pct
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", rerr
		}
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want {
		return "", fmt.Errorf("%s: checksum mismatch", p.Asset)
	}
	progress("installing", -1)
	staged := p.Target + ".new"
	os.RemoveAll(staged)
	if strings.HasSuffix(p.Asset, ".zip") {
		err = unzip(tmp, done, p, staged)
	} else {
		err = untar(tmp, p, staged)
	}
	if err != nil {
		os.RemoveAll(staged)
		return "", err
	}
	return staged, nil
}

// clean checks an archive entry's name and returns it relative to the
// member: "" for the member itself, ok false for anything outside it.
func clean(name, member string) (string, bool) {
	name = strings.TrimPrefix(path.Clean("/"+name), "/")
	if name == member {
		return "", true
	}
	if rest, ok := strings.CutPrefix(name, member+"/"); ok && rest != "" {
		return rest, true
	}
	return "", false
}

func unzip(f *os.File, size int64, p Plan, staged string) error {
	zr, err := zip.NewReader(f, size)
	if err != nil {
		return err
	}
	found := false
	for _, e := range zr.File {
		rel, ok := clean(e.Name, p.Member)
		if !ok {
			continue
		}
		found = true
		dst := staged
		if rel != "" {
			dst = filepath.Join(staged, filepath.FromSlash(rel))
		}
		mode := e.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case mode&fs.ModeSymlink != 0:
			if !p.Bundle {
				return fmt.Errorf("%s: unexpected link", e.Name)
			}
			r, err := e.Open()
			if err != nil {
				return err
			}
			link, err := io.ReadAll(io.LimitReader(r, 4096))
			r.Close()
			if err != nil {
				return err
			}
			if err := safeLink(staged, dst, string(link)); err != nil {
				return err
			}
		default:
			r, err := e.Open()
			if err != nil {
				return err
			}
			err = writeFile(dst, r, mode.Perm()|0o600)
			r.Close()
			if err != nil {
				return err
			}
		}
	}
	if !found {
		return fmt.Errorf("%s is not in %s", p.Member, p.Asset)
	}
	return nil
}

func untar(f *os.File, p Plan, staged string) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("%s is not in %s", p.Member, p.Asset)
		}
		if err != nil {
			return err
		}
		if rel, ok := clean(h.Name, p.Member); ok && rel == "" && h.Typeflag == tar.TypeReg {
			return writeFile(staged, tr, 0o755)
		}
	}
}

func writeFile(dst string, r io.Reader, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	w, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(w, io.LimitReader(r, maxAsset)); err != nil {
		w.Close()
		return err
	}
	return w.Close()
}

// safeLink makes a symbolic link inside an app bundle, refusing one that
// points outside it.
func safeLink(root, at, target string) error {
	if filepath.IsAbs(target) {
		return fmt.Errorf("link %s: absolute target", at)
	}
	resolved := filepath.Join(filepath.Dir(at), target)
	if r, err := filepath.Rel(root, resolved); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return fmt.Errorf("link %s: points outside the app", at)
	}
	if err := os.MkdirAll(filepath.Dir(at), 0o755); err != nil {
		return err
	}
	return os.Symlink(target, at)
}

// Install swaps the staged program in: the old one becomes Target+".old"
// (a running program can be renamed, even on Windows), which Cleanup
// removes at the next start. On failure the old program is put back.
func Install(p Plan, staged string) error {
	old := p.Target + ".old"
	os.RemoveAll(old)
	if err := os.Rename(p.Target, old); err != nil {
		return err
	}
	if err := os.Rename(staged, p.Target); err != nil {
		if rerr := os.Rename(old, p.Target); rerr != nil {
			return fmt.Errorf("%w (and restoring the old program failed: %v)", err, rerr)
		}
		return err
	}
	return nil
}

// Cleanup removes what an upgrade left behind (the old program, a staged
// one that was never installed). It is called at start; failures are
// harmless (the old program may still be exiting on Windows).
func Cleanup(p Plan) {
	for _, s := range []string{p.Target + ".old", p.Target + ".new"} {
		if _, err := os.Lstat(s); err == nil {
			os.RemoveAll(s)
		}
	}
}
