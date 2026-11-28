package ffmpeg

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/config"
)

// Progress reports an install's stage ("downloading", "verifying",
// "installing") and, while downloading, the bytes done of total.
type Progress func(stage string, done, total int64)

// Options adjust Install; the zero value installs this system's pinned
// build.
type Options struct {
	// Progress, if set, hears how far the install is.
	Progress Progress
	// Force installs again when the same build is already installed.
	Force bool
	// Client downloads; nil means a client that goes through the proxy
	// the environment names (HTTPS_PROXY).
	Client *http.Client

	// For tests: another build and directory, and what checks that the
	// program runs (nil: Version).
	build   *Build
	dir     string
	version func(ctx context.Context, path string) string
}

// Manifest describes RemoHop's copy (ffmpeg.json next to it).
type Manifest struct {
	Version   string    `json:"version"`
	Source    string    `json:"source"`
	URL       string    `json:"url"`
	SHA256    string    `json:"sha256"` // the archive's
	Installed time.Time `json:"installed"`
}

const manifestName = "ffmpeg.json"

// ErrUnsupported: there is no pinned build for this system.
var ErrUnsupported = errors.New("RemoHop has no ffmpeg build to download for " + runtime.GOOS + "/" + runtime.GOARCH +
	"; install ffmpeg with the system's package manager")

// Install downloads this system's pinned build, checks it, and puts its
// ffmpeg program in Dir. It returns the program's path. An unverified
// download is deleted and never opened as an archive, let alone run.
func Install(ctx context.Context, o Options) (string, error) {
	if off, by := DownloadDisabled(); off {
		return "", fmt.Errorf("downloading ffmpeg is switched off (%s): install ffmpeg yourself, or point %s at one", by, PathEnv)
	}
	b, ok := Pinned()
	if o.build != nil {
		b, ok = *o.build, true
	}
	if !ok {
		return "", ErrUnsupported
	}
	dir := o.dir
	if dir == "" {
		d, err := Dir()
		if err != nil {
			return "", err
		}
		dir = d
	}
	progress := o.Progress
	if progress == nil {
		progress = func(string, int64, int64) {}
	}
	// One install at a time, across processes (the app and the command
	// line, say).
	unlock, err := config.Lock(filepath.Join(dir, "install"))
	if err != nil {
		return "", fmt.Errorf("another ffmpeg install is under way: %w", err)
	}
	defer unlock()
	cleanTemp(dir)

	dst := filepath.Join(dir, exeName())
	if !o.Force {
		if m, err := readManifest(dir); err == nil && m.SHA256 == b.SHA256 {
			if fi, err := os.Stat(dst); err == nil && fi.Size() == b.MemberSize {
				return dst, nil
			}
		}
	}

	archive, used, err := download(ctx, o.Client, b, dir, progress)
	if err != nil {
		return "", err
	}
	defer os.Remove(archive)

	progress("installing", 0, 0)
	tmp, err := extract(archive, b, dir)
	os.Remove(archive)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp) // gone after the rename, unless that failed
	if err := unquarantine(tmp); err != nil {
		return "", fmt.Errorf("clearing the download quarantine of %s: %w", tmp, err)
	}
	if err := replace(tmp, dst); err != nil {
		return "", err
	}
	version := o.version
	if version == nil {
		version = Version
	}
	if v := version(ctx, dst); v == "" {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		os.Remove(dst)
		removeIfExists(filepath.Join(dir, manifestName))
		return "", fmt.Errorf("the downloaded ffmpeg %s does not run on this computer", b.Version)
	}
	m := Manifest{Version: b.Version, Source: b.Source, URL: used, SHA256: b.SHA256, Installed: time.Now().UTC().Truncate(time.Second)}
	data, _ := json.MarshalIndent(m, "", "  ")
	if err := config.WriteFileAtomic(filepath.Join(dir, manifestName), append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return dst, nil
}

// Remove deletes RemoHop's copy. It reports whether there was one.
func Remove() (bool, error) {
	dir, err := Dir()
	if err != nil {
		return false, err
	}
	unlock, err := config.Lock(filepath.Join(dir, "install"))
	if err != nil {
		return false, fmt.Errorf("an ffmpeg install is under way: %w", err)
	}
	defer unlock()
	cleanTemp(dir)
	p := filepath.Join(dir, exeName())
	_, statErr := os.Stat(p)
	if err := removeIfExists(p); err != nil {
		return false, err
	}
	removeIfExists(p + ".old")
	removeIfExists(filepath.Join(dir, manifestName))
	return statErr == nil, nil
}

// InstalledManifest describes RemoHop's copy, if there is one.
func InstalledManifest() (Manifest, bool) {
	if _, ok := installed(); !ok {
		return Manifest{}, false
	}
	dir, err := Dir()
	if err != nil {
		return Manifest{}, false
	}
	m, err := readManifest(dir)
	return m, err == nil
}

func readManifest(dir string) (Manifest, error) {
	var m Manifest
	b, err := config.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return m, err
	}
	return m, json.Unmarshal(b, &m)
}

// Temporary files in Dir; what a failed install left is removed by the
// next one.
const (
	archivePrefix = ".download-"
	programPrefix = ".program-"
)

func cleanTemp(dir string) {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if n := e.Name(); strings.HasPrefix(n, archivePrefix) || strings.HasPrefix(n, programPrefix) || n == exeName()+".old" {
			os.Remove(filepath.Join(dir, n))
		}
	}
}

// httpClient downloads through the proxy the environment names, and
// follows redirects only to HTTPS addresses (GitHub sends release files
// on to its storage).
func httpClient(c *http.Client) *http.Client {
	if c == nil {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.Proxy = http.ProxyFromEnvironment
		t.ResponseHeaderTimeout = time.Minute
		c = &http.Client{Transport: t, Timeout: 30 * time.Minute}
	}
	cc := *c
	cc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("refusing a redirect to %s: not HTTPS", req.URL.Redacted())
		}
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return nil
	}
	return &cc
}

// download fetches b from the first mirror that works into a temporary
// file in dir and checks its size and hash. It returns the file and the
// address it came from. A file that does not match is deleted.
func download(ctx context.Context, client *http.Client, b Build, dir string, progress Progress) (string, string, error) {
	c := httpClient(client)
	var errs []string
	for _, u := range b.URLs {
		p, err := fetch(ctx, c, u, b, dir, progress)
		if err == nil {
			return p, u, nil
		}
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		errs = append(errs, err.Error())
	}
	return "", "", fmt.Errorf("downloading ffmpeg %s: %s", b.Version, strings.Join(errs, "; "))
}

func fetch(ctx context.Context, c *http.Client, rawURL string, b Build, dir string, progress Progress) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return "", fmt.Errorf("%s: not an HTTPS address", rawURL)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "RemoHop")
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: HTTP %d", u.Host, resp.StatusCode)
	}
	f, err := os.CreateTemp(dir, archivePrefix+"*.zip")
	if err != nil {
		return "", err
	}
	name := f.Name()
	fail := func(err error) (string, error) {
		f.Close()
		os.Remove(name)
		return "", err
	}
	h := sha256.New()
	// Never more than the pinned size: one byte over is already wrong.
	body := io.LimitReader(resp.Body, b.Size+1)
	var done int64
	buf := make([]byte, 256<<10)
	last := time.Time{}
	progress("downloading", 0, b.Size)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			done += int64(n)
			h.Write(buf[:n])
			if _, err := f.Write(buf[:n]); err != nil {
				return fail(err)
			}
			if now := time.Now(); now.Sub(last) >= 200*time.Millisecond {
				progress("downloading", min(done, b.Size), b.Size)
				last = now
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fail(fmt.Errorf("%s: %w", u.Host, rerr))
		}
	}
	progress("verifying", done, b.Size)
	if done != b.Size {
		return fail(fmt.Errorf("%s: the file is %d bytes, not the %d expected; refusing it", u.Host, done, b.Size))
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != b.SHA256 {
		return fail(fmt.Errorf("%s: the file's SHA-256 is %s, not the %s pinned in RemoHop; refusing it", u.Host, got, b.SHA256))
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// extract copies b.Member out of the verified zip file archive into a new
// temporary file in dir, executable. Only an entry named exactly b.Member,
// a regular file of b.MemberSize bytes, is taken; nothing else in the
// archive is written anywhere, so its other names cannot point outside
// dir.
func extract(archive string, b Build, dir string) (string, error) {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return "", fmt.Errorf("reading the ffmpeg archive: %w", err)
	}
	defer zr.Close()
	var member *zip.File
	for _, e := range zr.File {
		if e.Name != b.Member {
			continue
		}
		if member != nil {
			return "", fmt.Errorf("%s appears twice in the archive", b.Member)
		}
		member = e
	}
	if member == nil {
		return "", fmt.Errorf("%s is not in the archive", b.Member)
	}
	if !member.Mode().IsRegular() {
		return "", fmt.Errorf("%s in the archive is not a regular file", b.Member)
	}
	if member.UncompressedSize64 != uint64(b.MemberSize) {
		return "", fmt.Errorf("%s in the archive is %d bytes, not %d", b.Member, member.UncompressedSize64, b.MemberSize)
	}
	r, err := member.Open()
	if err != nil {
		return "", err
	}
	defer r.Close()
	f, err := os.CreateTemp(dir, programPrefix+"*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	fail := func(err error) (string, error) {
		f.Close()
		os.Remove(name)
		return "", err
	}
	// The header's size is the archive's word for it: read one byte
	// past it to be sure.
	n, err := io.Copy(f, io.LimitReader(r, b.MemberSize+1))
	if err != nil {
		return fail(fmt.Errorf("extracting %s: %w", b.Member, err))
	}
	if n != b.MemberSize {
		return fail(fmt.Errorf("%s in the archive is not %d bytes", b.Member, b.MemberSize))
	}
	if err := f.Chmod(0o755); err != nil && runtime.GOOS != "windows" {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// replace puts the new program at dst. An old one is moved aside first:
// Windows cannot overwrite a running program but can rename it. What is
// left aside is removed now or by the next install.
func replace(tmp, dst string) error {
	old := dst + ".old"
	removeIfExists(old)
	if _, err := os.Stat(dst); err == nil {
		if err := config.Rename(dst, old); err != nil {
			return fmt.Errorf("moving the old ffmpeg aside: %w", err)
		}
	}
	if err := config.Rename(tmp, dst); err != nil {
		config.Rename(old, dst)
		return err
	}
	removeIfExists(old)
	return nil
}
