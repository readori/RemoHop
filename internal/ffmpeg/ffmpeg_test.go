package ffmpeg

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
)

// isolate gives a test its own RemoHop directory, an empty PATH and no
// REMOHOP_FFMPEG or download switch, and pretends this system has a pinned
// build (whatever it is), so that RemoHop's copy is looked for.
func isolate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", dir)
	t.Setenv("PATH", t.TempDir())
	t.Setenv(PathEnv, "")
	t.Setenv(DownloadEnv, "")
	old := pinned
	pinned = func() (Build, bool) { return Build{Version: "test", Size: 1}, true }
	t.Cleanup(func() { pinned = old })
	return dir
}

func writeExe(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho ffmpeg version test\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestFindPrecedence: REMOHOP_FFMPEG, then PATH (the system's), then
// RemoHop's copy, and RemoHop's copy only where RemoHop has a build.
func TestFindPrecedence(t *testing.T) {
	isolate(t)
	if _, err := Find(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("nothing anywhere: %v", err)
	}

	own, err := path()
	if err != nil {
		t.Fatal(err)
	}
	writeExe(t, own)
	if f, err := Find(); err != nil || f.Kind != KindRemoHop || f.Path != own {
		t.Fatalf("RemoHop's copy only: %+v %v", f, err)
	}

	bin := t.TempDir()
	t.Setenv("PATH", bin)
	writeExe(t, filepath.Join(bin, exeName()))
	if f, err := Find(); err != nil || f.Kind != KindSystem || filepath.Dir(f.Path) != bin {
		t.Fatalf("the system's wins over RemoHop's: %+v %v", f, err)
	}

	t.Setenv(PathEnv, "/opt/elsewhere/ffmpeg")
	if f, err := Find(); err != nil || f.Kind != KindEnv || f.Path != "/opt/elsewhere/ffmpeg" {
		t.Fatalf("REMOHOP_FFMPEG wins, as it is: %+v %v", f, err)
	}

	// Linux (no pinned build): RemoHop's copy is not looked for.
	t.Setenv(PathEnv, "")
	t.Setenv("PATH", t.TempDir())
	pinned = func() (Build, bool) { return Build{}, false }
	if _, err := Find(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("no pinned build: RemoHop's copy must not be used: %v", err)
	}
}

// TestDownloadSwitch: REMOHOP_FFMPEG_DOWNLOAD=off and the setting switch
// downloads off; the variable wins.
func TestDownloadSwitch(t *testing.T) {
	isolate(t)
	if off, _ := DownloadDisabled(); off {
		t.Fatal("downloads are on by default")
	}
	if InstallHint() == "" {
		t.Error("no install hint where a build is pinned")
	}
	t.Setenv(DownloadEnv, "off")
	if off, by := DownloadDisabled(); !off || !strings.Contains(by, DownloadEnv) {
		t.Errorf("env off: %v %q", off, by)
	}
	if InstallHint() != "" {
		t.Error("the hint offers a download that is switched off")
	}
	t.Setenv(DownloadEnv, "")
	if err := config.Set(SettingDownload, "off"); err != nil {
		t.Fatal(err)
	}
	if off, by := DownloadDisabled(); !off || !strings.Contains(by, SettingDownload) {
		t.Errorf("setting off: %v %q", off, by)
	}
	t.Setenv(DownloadEnv, "on")
	if off, _ := DownloadDisabled(); off {
		t.Error("the variable should win over the setting")
	}
}

// archive is a zip file for tests.
type entry struct {
	name string
	data []byte
	mode fs.FileMode
}

func makeZip(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		mode := e.mode
		if mode == 0 {
			mode = 0o755
		}
		h.SetMode(mode)
		w, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		w.Write(e.data)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func buildOf(zipData []byte, member string, memberSize int, urls ...string) Build {
	sum := sha256.Sum256(zipData)
	return Build{Version: "test", Source: "a test server", URLs: urls, SHA256: hex.EncodeToString(sum[:]),
		Size: int64(len(zipData)), Member: member, MemberSize: int64(memberSize)}
}

// server serves files by path over HTTPS and counts requests.
type server struct {
	*httptest.Server
	files map[string][]byte
	hits  atomic.Int32
}

func newServer(t *testing.T) *server {
	s := &server{files: map[string][]byte{}}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		b, ok := s.files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(s.Close)
	return s
}

func testOptions(s *server, b Build, dir string) Options {
	return Options{Client: s.Client(), build: &b, dir: dir,
		version: func(context.Context, string) string { return "ffmpeg version test" }}
}

// leftovers lists what is in dir besides the lock file.
func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if e.Name() != "install.lock" {
			out = append(out, e.Name())
		}
	}
	return out
}

var program = []byte("\x7fELF a pretend ffmpeg program")

// TestInstall: the archive is fetched, checked and only the program is
// taken out of it; names elsewhere in the archive (outside its directory,
// absolute) are never written. A second install of the same build fetches
// nothing; --force does.
func TestInstall(t *testing.T) {
	isolate(t)
	s := newServer(t)
	member := "ffmpeg-test/bin/" + exeName()
	s.files["/a.zip"] = makeZip(t,
		entry{name: "ffmpeg-test/README.txt", data: []byte("readme")},
		entry{name: "../escaped", data: []byte("nope")},
		entry{name: "/abs/escaped", data: []byte("nope")},
		entry{name: "ffmpeg-test/bin/../../../escaped", data: []byte("nope")},
		entry{name: member, data: program},
		entry{name: "ffmpeg-test/bin/ffprobe", data: []byte("not wanted")},
	)
	b := buildOf(s.files["/a.zip"], member, len(program), s.URL+"/a.zip")
	parent := t.TempDir()
	dir := filepath.Join(parent, "ffmpeg")
	os.Mkdir(dir, 0o700)
	var stages []string
	o := testOptions(s, b, dir)
	o.Progress = func(stage string, done, total int64) {
		if len(stages) == 0 || stages[len(stages)-1] != stage {
			stages = append(stages, stage)
		}
	}
	got, err := Install(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, exeName()); got != want {
		t.Errorf("installed at %s, want %s", got, want)
	}
	data, err := os.ReadFile(got)
	if err != nil || !bytes.Equal(data, program) {
		t.Fatalf("installed program: %q %v", data, err)
	}
	if fi, _ := os.Stat(got); runtime.GOOS != "windows" && fi.Mode().Perm()&0o111 == 0 {
		t.Errorf("not executable: %v", fi.Mode())
	}
	if strings.Join(stages, ",") != "downloading,verifying,installing" {
		t.Errorf("progress stages: %v", stages)
	}
	if l := leftovers(t, dir); strings.Join(l, ",") != exeName()+","+manifestName {
		t.Errorf("left in the directory: %v", l)
	}
	if l := leftovers(t, parent); len(l) != 1 {
		t.Errorf("something was written next to the directory: %v", l)
	}
	m, err := readManifest(dir)
	if err != nil || m.SHA256 != b.SHA256 || m.URL != s.URL+"/a.zip" || m.Version != "test" {
		t.Errorf("manifest: %+v %v", m, err)
	}

	hits := s.hits.Load()
	if _, err := Install(context.Background(), o); err != nil || s.hits.Load() != hits {
		t.Errorf("the same build again: err %v, %d more requests", err, s.hits.Load()-hits)
	}
	o.Force = true
	if _, err := Install(context.Background(), o); err != nil || s.hits.Load() != hits+1 {
		t.Errorf("--force: err %v, %d more requests", err, s.hits.Load()-hits)
	}
}

// TestHashMismatch: a download that does not match the pin is refused and
// deleted, and an installed copy stays as it was.
func TestHashMismatch(t *testing.T) {
	isolate(t)
	s := newServer(t)
	s.files["/a.zip"] = makeZip(t, entry{name: "ffmpeg", data: program})
	b := buildOf(s.files["/a.zip"], "ffmpeg", len(program), s.URL+"/a.zip")
	b.SHA256 = strings.Repeat("0", 64)
	dir := t.TempDir()
	old := filepath.Join(dir, exeName())
	os.WriteFile(old, []byte("the old one"), 0o755)
	_, err := Install(context.Background(), testOptions(s, b, dir))
	if err == nil || !strings.Contains(err.Error(), "SHA-256") || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("a wrong hash must be refused: %v", err)
	}
	if l := leftovers(t, dir); strings.Join(l, ",") != exeName() {
		t.Errorf("the download was not deleted: %v", l)
	}
	if data, _ := os.ReadFile(old); string(data) != "the old one" {
		t.Errorf("the installed copy changed: %q", data)
	}

	// Longer than pinned: refused, without reading on.
	b = buildOf(s.files["/a.zip"], "ffmpeg", len(program), s.URL+"/a.zip")
	b.Size--
	if _, err := Install(context.Background(), testOptions(s, b, dir)); err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Errorf("a file of another size must be refused: %v", err)
	}
	if l := leftovers(t, dir); strings.Join(l, ",") != exeName() {
		t.Errorf("the download was not deleted: %v", l)
	}
}

// TestMirrors: a mirror that fails is skipped for the next one; only HTTPS
// is used, redirects included.
func TestMirrors(t *testing.T) {
	isolate(t)
	s := newServer(t)
	s.files["/a.zip"] = makeZip(t, entry{name: "ffmpeg", data: program})
	b := buildOf(s.files["/a.zip"], "ffmpeg", len(program), s.URL+"/gone.zip", s.URL+"/a.zip")
	dir := t.TempDir()
	if _, err := Install(context.Background(), testOptions(s, b, dir)); err != nil {
		t.Fatal(err)
	}
	if m, _ := readManifest(dir); m.URL != s.URL+"/a.zip" {
		t.Errorf("manifest URL %q", m.URL)
	}

	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(s.files["/a.zip"]) }))
	defer plain.Close()
	b = buildOf(s.files["/a.zip"], "ffmpeg", len(program), plain.URL+"/a.zip")
	if _, err := Install(context.Background(), Options{Force: true, build: &b, dir: t.TempDir()}); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Errorf("plain HTTP must be refused: %v", err)
	}
	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/a.zip", http.StatusFound)
	}))
	defer redirect.Close()
	b = buildOf(s.files["/a.zip"], "ffmpeg", len(program), redirect.URL+"/a.zip")
	o := Options{Force: true, Client: redirect.Client(), build: &b, dir: t.TempDir()}
	if _, err := Install(context.Background(), o); err == nil || !strings.Contains(err.Error(), "not HTTPS") {
		t.Errorf("a redirect to plain HTTP must be refused: %v", err)
	}
}

// TestDownloadOff: nothing is fetched while downloads are switched off.
func TestDownloadOff(t *testing.T) {
	isolate(t)
	t.Setenv(DownloadEnv, "off")
	s := newServer(t)
	s.files["/a.zip"] = makeZip(t, entry{name: "ffmpeg", data: program})
	b := buildOf(s.files["/a.zip"], "ffmpeg", len(program), s.URL+"/a.zip")
	_, err := Install(context.Background(), testOptions(s, b, t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "switched off") || s.hits.Load() != 0 {
		t.Errorf("switched off: err %v, %d requests", err, s.hits.Load())
	}
	in := &Installer{Find: func() (Found, error) { return Found{}, ErrNotFound }}
	st := in.State()
	if !st.Missing || st.CanInstall || !strings.Contains(st.Disabled, DownloadEnv) {
		t.Errorf("the UI offers a switched-off download: %+v", st)
	}
	if err := in.Start(context.Background()); err == nil {
		t.Error("the UI started a switched-off download")
	}
}

// TestExtract: only the named member, a regular file of the pinned size,
// is taken; an archive that lies about it is refused.
func TestExtract(t *testing.T) {
	isolate(t)
	cases := []struct {
		name    string
		zip     func() []byte
		member  string
		size    int
		wantErr string
	}{
		{"missing", func() []byte { return makeZip(t, entry{name: "other", data: program}) }, "ffmpeg", len(program), "not in the archive"},
		{"only outside names", func() []byte {
			return makeZip(t, entry{name: "../ffmpeg", data: program}, entry{name: "/ffmpeg", data: program}, entry{name: "x/../ffmpeg", data: program})
		}, "ffmpeg", len(program), "not in the archive"},
		{"twice", func() []byte {
			return makeZip(t, entry{name: "ffmpeg", data: program}, entry{name: "ffmpeg", data: program})
		}, "ffmpeg", len(program), "twice"},
		{"a link", func() []byte {
			return makeZip(t, entry{name: "ffmpeg", data: []byte("/bin/sh"), mode: fs.ModeSymlink | 0o777})
		}, "ffmpeg", 7, "not a regular file"},
		{"a directory", func() []byte { return makeZip(t, entry{name: "ffmpeg/", mode: fs.ModeDir | 0o755}) }, "ffmpeg/", 0, "not a regular file"},
		{"other size", func() []byte { return makeZip(t, entry{name: "ffmpeg", data: program}) }, "ffmpeg", len(program) - 1, "bytes, not"},
		{"header lies", func() []byte {
			// Says 10 bytes, holds 1 MB: the copy stops at once
			// (archive/zip notices, and the copy never reads more
			// than one byte past the pinned size anyway).
			var buf bytes.Buffer
			zw := zip.NewWriter(&buf)
			big := bytes.Repeat([]byte("x"), 1<<20)
			h := &zip.FileHeader{Name: "ffmpeg", Method: zip.Store, CompressedSize64: uint64(len(big)), UncompressedSize64: 10}
			h.SetMode(0o755)
			w, _ := zw.CreateRaw(h)
			w.Write(big)
			zw.Close()
			return buf.Bytes()
		}, "ffmpeg", 10, "extracting ffmpeg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "d")
			os.Mkdir(dir, 0o700)
			archive := filepath.Join(t.TempDir(), "a.zip")
			data := c.zip()
			os.WriteFile(archive, data, 0o600)
			_, err := extract(archive, buildOf(data, c.member, c.size), dir)
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("want an error with %q, got %v", c.wantErr, err)
			}
			if l := leftovers(t, dir); len(l) != 0 {
				t.Errorf("left in the directory: %v", l)
			}
			if l := leftovers(t, parent); len(l) != 1 {
				t.Errorf("written next to the directory: %v", l)
			}
		})
	}
}

// TestInstaller: the UI's install runs in the background and reports its
// stages, its failure, and the ffmpeg it found.
func TestInstaller(t *testing.T) {
	isolate(t)
	have := atomic.Bool{}
	release := make(chan error)
	changes := atomic.Int32{}
	in := &Installer{
		OnChange: func() { changes.Add(1) },
		Find: func() (Found, error) {
			if have.Load() {
				return Found{Path: "ffmpeg", Kind: KindRemoHop}, nil
			}
			return Found{}, ErrNotFound
		},
		Pinned: func() (Build, bool) { return Build{Version: "9.1", Source: "test", Size: 119_467_219}, true },
		Install: func(ctx context.Context, p Progress) (string, error) {
			p("downloading", 25, 100)
			if err := <-release; err != nil {
				return "", err
			}
			have.Store(true)
			return "ffmpeg", nil
		},
	}
	st := in.State()
	if !st.Missing || !st.CanInstall || st.SizeMB != 119 || st.Version != "9.1" {
		t.Fatalf("before: %+v", st)
	}
	if err := in.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return in.State().Percent == 25 })
	if st := in.State(); st.Stage != "downloading" {
		t.Errorf("during: %+v", st)
	}
	if err := in.Start(context.Background()); !errors.Is(err, ErrBusy) {
		t.Errorf("a second install at once: %v", err)
	}
	release <- errors.New("no network")
	waitFor(t, func() bool { return in.State().Stage == "" })
	if st := in.State(); st.Error != "no network" || !st.Missing {
		t.Errorf("after a failure: %+v", st)
	}
	if err := in.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	release <- nil
	waitFor(t, func() bool { st := in.State(); return st.Stage == "" && !st.Missing })
	if st := in.State(); st != (UIState{}) {
		t.Errorf("installed: %+v", st)
	}
	if changes.Load() < 4 {
		t.Errorf("only %d changes reported", changes.Load())
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("timed out")
}

// TestCommand: `remohop ffmpeg` says what is used and what install would
// fetch; install refuses while downloads are switched off.
func TestCommand(t *testing.T) {
	isolate(t)
	pinned = func() (Build, bool) {
		return Build{Version: "9.1", Source: "a test", License: "GPL-3.0", Size: 119_467_219, SHA256: "ab"}, true
	}
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := cli.Main(context.Background(), args, strings.NewReader(""), &out, &errb)
		return code, out.String(), errb.String()
	}
	code, out, _ := run("ffmpeg")
	if code != 0 || !strings.Contains(out, "not found") || !strings.Contains(out, "`remohop ffmpeg install` downloads ffmpeg 9.1 from a test (GPL-3.0, about 119 MB)") {
		t.Errorf("status: %d %q", code, out)
	}
	code, out, _ = run("ffmpeg", "status", "--json")
	var st Status
	if err := json.Unmarshal([]byte(out), &st); code != 0 || err != nil || st.Found != nil || st.Pinned == nil || st.Pinned.SizeMB != 119 {
		t.Errorf("status --json: %d %v %q", code, err, out)
	}
	t.Setenv(DownloadEnv, "off")
	if code, _, errs := run("ffmpeg", "install"); code != cli.ExitError || !strings.Contains(errs, "switched off") {
		t.Errorf("install while switched off: %d %q", code, errs)
	}
	if code, out, _ := run("ffmpeg", "remove"); code != 0 || !strings.Contains(out, "no copy") {
		t.Errorf("remove: %d %q", code, out)
	}
	if code, _, _ := run("ffmpeg", "install", "extra"); code != cli.ExitUsage {
		t.Errorf("an extra argument: %d", code)
	}
}
