package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSignVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	other, _, _ := ed25519.GenerateKey(nil)
	sums := []byte("aa  x\n")
	sig := Sign(priv, "v1.0.3", sums)
	if err := Verify([]ed25519.PublicKey{other, pub}, "v1.0.3", sums, sig); err != nil {
		t.Fatal(err)
	}
	for name, err := range map[string]error{
		"other tag (replay of an older release)": Verify([]ed25519.PublicKey{pub}, "v1.0.4", sums, sig),
		"other sums":                             Verify([]ed25519.PublicKey{pub}, "v1.0.3", []byte("bb  x\n"), sig),
		"untrusted key":                          Verify([]ed25519.PublicKey{other}, "v1.0.3", sums, sig),
		"garbage":                                Verify([]ed25519.PublicKey{pub}, "v1.0.3", sums, []byte("nope")),
		"no keys":                                Verify(nil, "v1.0.3", sums, sig),
	} {
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTrustedKeys(t *testing.T) {
	for _, s := range TrustedKeys {
		if _, err := ParseKey(s); err != nil {
			t.Error(err)
		}
	}
	_ = Keys()
}

func TestPlanFor(t *testing.T) {
	for _, c := range []struct {
		goos, arch, exe string
		asset, target   string
		bundle          bool
	}{
		{"windows", "amd64", `C:\Users\a\Downloads\RemoHop\RemoHop.exe`, "RemoHop-windows-amd64.zip", `C:\Users\a\Downloads\RemoHop\RemoHop.exe`, false},
		{"windows", "arm64", `C:\Users\a\AppData\Local\Programs\RemoHop\remohop.exe`, "remohop_windows_arm64.zip", `C:\Users\a\AppData\Local\Programs\RemoHop\remohop.exe`, false},
		{"darwin", "arm64", "/Applications/RemoHop.app/Contents/MacOS/RemoHop", "RemoHop-macos.zip", "/Applications/RemoHop.app", true},
		{"darwin", "amd64", "/Users/a/.local/bin/remohop", "remohop_darwin_universal.tar.gz", "/Users/a/.local/bin/remohop", false},
		{"linux", "amd64", "/home/a/remohop-app", "remohop-app_linux_amd64.tar.gz", "/home/a/remohop-app", false},
		{"linux", "arm64", "/home/a/.local/bin/remohop", "remohop_linux_arm64.tar.gz", "/home/a/.local/bin/remohop", false},
	} {
		p, err := PlanFor(c.goos, c.arch, c.exe)
		if err != nil || p.Asset != c.asset || p.Target != c.target || p.Bundle != c.bundle {
			t.Errorf("PlanFor(%s %s) = %+v, %v", c.goos, c.exe, p, err)
		}
	}
	for _, c := range [][3]string{
		{"windows", "amd64", `C:\x\RemoHop-QuickSupport.exe`},
		{"darwin", "arm64", "/Applications/RemoHop Quick Support.app/Contents/MacOS/RemoHop Quick Support"},
		{"linux", "amd64", "/home/a/remohop-quick"},
		{"linux", "386", "/home/a/remohop"},
		{"freebsd", "amd64", "/usr/local/bin/remohop"},
	} {
		if _, err := PlanFor(c[0], c[1], c[2]); !errors.Is(err, ErrUnsupported) {
			t.Errorf("PlanFor(%v) = %v, want unsupported", c, err)
		}
	}
}

// release serves a fake release: SHA256SUMS signed with priv for tag, and
// the given files.
func release(t *testing.T, priv ed25519.PrivateKey, tag string, files map[string][]byte) *httptest.Server {
	t.Helper()
	var sums strings.Builder
	for name, b := range files {
		h := sha256.Sum256(b)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(h[:]), name)
	}
	all := map[string][]byte{SumsFile: []byte(sums.String()), SigFile: Sign(priv, tag, []byte(sums.String()))}
	for k, v := range files {
		all[k] = v
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := all[strings.TrimPrefix(r.URL.Path, "/"+tag+"/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func tgz(t *testing.T, name string, content []byte) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "LICENSE", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg})
	tw.Write([]byte("lic"))
	tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg})
	tw.Write(content)
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func zipOf(t *testing.T, entries map[string]string, links map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		h := &zip.FileHeader{Name: name, Method: zip.Deflate}
		h.SetMode(0o755)
		w, _ := zw.CreateHeader(h)
		w.Write([]byte(content))
	}
	for name, target := range links {
		h := &zip.FileHeader{Name: name}
		h.SetMode(os.ModeSymlink | 0o777)
		w, _ := zw.CreateHeader(h)
		w.Write([]byte(target))
	}
	zw.Close()
	return buf.Bytes()
}

func TestDownloadAndInstall(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	keys := []ed25519.PublicKey{pub}
	dir := t.TempDir()
	exe := filepath.Join(dir, "remohop-app")
	os.WriteFile(exe, []byte("old program"), 0o755)
	p := Plan{Asset: "remohop-app_linux_amd64.tar.gz", Member: "remohop-app", Target: exe}
	if !p.Writable() {
		t.Fatal("temp dir not writable")
	}
	srv := release(t, priv, "v1.0.3", map[string][]byte{p.Asset: tgz(t, "remohop-app", []byte("new program"))})
	f := Fetcher{Client: srv.Client(), Base: srv.URL + "/%s"}

	var stages []string
	staged, err := Download(context.Background(), f, keys, "v1.0.3", p, func(s string, _ int) {
		if len(stages) == 0 || stages[len(stages)-1] != s {
			stages = append(stages, s)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(stages, ","); got != "verifying,downloading,installing" {
		t.Errorf("stages %s", got)
	}
	if b, _ := os.ReadFile(exe); string(b) != "old program" {
		t.Fatal("Download changed the program")
	}
	if err := Install(p, staged); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new program" {
		t.Fatalf("installed: %q", b)
	}
	if st, _ := os.Stat(exe); st.Mode().Perm()&0o100 == 0 {
		t.Error("not executable")
	}
	if b, _ := os.ReadFile(exe + ".old"); string(b) != "old program" {
		t.Error("the old program was not kept until the next start")
	}
	Cleanup(p)
	if _, err := os.Stat(exe + ".old"); !os.IsNotExist(err) {
		t.Error("Cleanup left the old program")
	}

	// A release signed by another key, an older release's files under a
	// newer tag, and a file that does not match SHA256SUMS are refused,
	// leaving the program alone.
	_, evil, _ := ed25519.GenerateKey(nil)
	bad := release(t, evil, "v1.0.4", map[string][]byte{p.Asset: tgz(t, "remohop-app", []byte("evil"))})
	if _, err := Download(context.Background(), Fetcher{Client: bad.Client(), Base: bad.URL + "/%s"}, keys, "v1.0.4", p, nil); !errors.Is(err, ErrBadSignature) {
		t.Errorf("foreign signature: %v", err)
	}
	replay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = strings.Replace(r.URL.Path, "/v1.0.5/", "/v1.0.3/", 1)
		srv.Config.Handler.ServeHTTP(w, r)
	}))
	defer replay.Close()
	if _, err := Download(context.Background(), Fetcher{Client: replay.Client(), Base: replay.URL + "/%s"}, keys, "v1.0.5", p, nil); !errors.Is(err, ErrBadSignature) {
		t.Errorf("replayed release: %v", err)
	}
	tampered := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, p.Asset) {
			w.Write(tgz(t, "remohop-app", []byte("tampered")))
			return
		}
		srv.Config.Handler.ServeHTTP(w, r)
	}))
	defer tampered.Close()
	if _, err := Download(context.Background(), Fetcher{Client: tampered.Client(), Base: tampered.URL + "/%s"}, keys, "v1.0.3", p, nil); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("tampered archive: %v", err)
	}
	if b, _ := os.ReadFile(exe); string(b) != "new program" {
		t.Fatal("a refused download changed the program")
	}
	if left, _ := filepath.Glob(filepath.Join(dir, ".remohop-update-*")); len(left) != 0 {
		t.Errorf("temporary files left: %v", left)
	}
}

func TestDownloadBundle(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	dir := t.TempDir()
	app := filepath.Join(dir, "RemoHop.app")
	os.MkdirAll(filepath.Join(app, "Contents", "MacOS"), 0o755)
	os.WriteFile(filepath.Join(app, "Contents", "MacOS", "RemoHop"), []byte("old"), 0o755)
	p := Plan{Asset: "RemoHop-macos.zip", Member: "RemoHop.app", Target: app, Bundle: true}

	good := zipOf(t, map[string]string{
		"RemoHop.app/Contents/MacOS/RemoHop": "new",
		"RemoHop.app/Contents/Info.plist":    "plist",
		"Other.app/Contents/MacOS/x":         "not ours",
	}, map[string]string{"RemoHop.app/Contents/Current": "MacOS"})
	srv := release(t, priv, "v1.0.3", map[string][]byte{p.Asset: good})
	staged, err := Download(context.Background(), Fetcher{Client: srv.Client(), Base: srv.URL + "/%s"}, []ed25519.PublicKey{pub}, "v1.0.3", p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := Install(p, staged); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(app, "Contents", "MacOS", "RemoHop")); string(b) != "new" {
		t.Fatalf("bundle: %q", b)
	}
	if l, _ := os.Readlink(filepath.Join(app, "Contents", "Current")); l != "MacOS" {
		t.Errorf("link: %q", l)
	}
	if _, err := os.Stat(filepath.Join(dir, "Other.app")); !os.IsNotExist(err) {
		t.Error("an entry outside the member was unpacked")
	}

	// A link that leaves the bundle is refused.
	evil := zipOf(t, map[string]string{"RemoHop.app/Contents/MacOS/RemoHop": "x"}, map[string]string{"RemoHop.app/Contents/escape": "../../../etc"})
	srv2 := release(t, priv, "v1.0.4", map[string][]byte{p.Asset: evil})
	if _, err := Download(context.Background(), Fetcher{Client: srv2.Client(), Base: srv2.URL + "/%s"}, []ed25519.PublicKey{pub}, "v1.0.4", p, nil); err == nil {
		t.Error("a link out of the bundle was accepted")
	}
	if _, err := os.Stat(app + ".new"); !os.IsNotExist(err) {
		t.Error("a failed download left the staged bundle")
	}
}

func TestClean(t *testing.T) {
	for _, c := range []struct {
		name, rel string
		ok        bool
	}{
		{"RemoHop.app", "", true},
		{"RemoHop.app/Contents/x", "Contents/x", true},
		{"RemoHop.app/../etc/passwd", "", false},
		{"/RemoHop.app/a", "a", true},
		{"RemoHop.appx/a", "", false},
		{"../RemoHop.app/a", "a", true}, // cleaned to RemoHop.app/a, still inside
	} {
		rel, ok := clean(c.name, "RemoHop.app")
		if rel != c.rel || ok != c.ok {
			t.Errorf("clean(%q) = %q, %v", c.name, rel, ok)
		}
	}
}
