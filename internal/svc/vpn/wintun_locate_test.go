package vpn

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The pinned hashes are those of wintun-0.14.1.zip as published on
// wintun.net (the zip's own hash is wintunZipSHA256).
func TestWintunPins(t *testing.T) {
	for arch, b := range wintunBuilds {
		if raw, err := hex.DecodeString(b.sha256); err != nil || len(raw) != 32 {
			t.Errorf("%s: malformed hash %q", arch, b.sha256)
		}
	}
	if len(wintunZipSHA256) != 64 || !strings.Contains(wintunURL, wintunVersion) {
		t.Error("zip pin")
	}
}

func TestLocateWintun(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	private := t.TempDir()
	empty, wrongDir, goodDir := t.TempDir(), t.TempDir(), t.TempDir()

	// Missing: the message says where to get it, how to check it, where to
	// put it, and that remohop will not fetch it.
	_, err := locateWintun([]string{empty, ""}, private, "amd64")
	if err == nil {
		t.Fatal("no DLL anywhere, yet located")
	}
	for _, want := range []string{"https://www.wintun.net", wintunURL, wintunZipSHA256, "0.14.1", `wintun\bin\amd64\wintun.dll`,
		"never downloads", "next to remohop.exe", "config directory", "certutil -hashfile"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing-driver message lacks %q:\n%s", want, err)
		}
	}

	// A file of that name that is not the expected build is refused and
	// named, whatever directory it is in.
	os.WriteFile(filepath.Join(wrongDir, "wintun.dll"), []byte("MZ not wintun"), 0o644)
	_, err = locateWintun([]string{wrongDir}, private, "amd64")
	if err == nil || !strings.Contains(err.Error(), filepath.Join(wrongDir, "wintun.dll")) || !strings.Contains(err.Error(), "not the Wintun 0.14.1 build") {
		t.Fatalf("wrong DLL: %v", err)
	}
	if ents, _ := os.ReadDir(private); len(ents) != 0 {
		t.Fatal("a refused DLL was copied")
	}

	// The expected build (here: a stand-in whose hash the test pins) is
	// copied into the private directory and loaded from there; the copy
	// is used on later runs even when the original is gone.
	fake := []byte("MZ stand-in for wintun.dll")
	sum := sha256.Sum256(fake)
	old := wintunBuilds["amd64"]
	wintunBuilds["amd64"] = struct{ dir, sha256 string }{"amd64", hex.EncodeToString(sum[:])}
	defer func() { wintunBuilds["amd64"] = old }()
	os.WriteFile(filepath.Join(goodDir, "wintun.dll"), fake, 0o644)
	path, err := locateWintun([]string{wrongDir, goodDir}, private, "amd64")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if filepath.Dir(path) != private || filepath.Base(path) != "wintun-0.14.1-amd64.dll" {
		t.Fatalf("loaded from %s, want the private copy", path)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, fake) {
		t.Fatal("copy differs")
	}
	os.Remove(filepath.Join(goodDir, "wintun.dll"))
	if again, err := locateWintun(nil, private, "amd64"); err != nil || again != path {
		t.Fatalf("second run: %q %v", again, err)
	}
	// A tampered private copy is not trusted.
	os.WriteFile(path, []byte("MZ tampered"), 0o600)
	if _, err := locateWintun(nil, private, "amd64"); err == nil {
		t.Fatal("tampered private copy accepted")
	}
	if _, err := locateWintun(nil, private, "mips"); err == nil {
		t.Fatal("an architecture without a Wintun build was accepted")
	}
	// Oversized files are not read in full.
	big := t.TempDir()
	os.WriteFile(filepath.Join(big, "wintun.dll"), make([]byte, wintunMaxSize+1), 0o644)
	if _, err := locateWintun([]string{big}, private, "amd64"); err == nil {
		t.Fatal("oversized DLL accepted")
	}
}

func TestWintunSearchOrder(t *testing.T) {
	t.Setenv("REMOHOP_WINTUN_DIR", "/from/env")
	cfg := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", cfg)
	t.Setenv("PATH", strings.Join([]string{"/p1", "/p2"}, string(os.PathListSeparator)))
	dirs := wintunSearchDirs()
	exe, _ := os.Executable()
	want := []string{"/from/env", filepath.Dir(exe), cfg, "/p1", "/p2"}
	if strings.Join(dirs, "|") != strings.Join(want, "|") {
		t.Fatalf("search order %v, want %v", dirs, want)
	}
}
