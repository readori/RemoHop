package handoff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSystem points the seams at a temporary tree: a home directory, snapd
// and Flatpak desktop file directories, and a /snap/bin.
func fakeSystem(t *testing.T, defaultID string) (home, root string) {
	t.Helper()
	root = t.TempDir()
	home = filepath.Join(root, "home")
	for _, d := range []string{home, filepath.Join(root, "snapd"), filepath.Join(root, "flatpak"), filepath.Join(root, "snapbin")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldSnapDesk, oldFlat, oldBin, oldID, oldHome := snapDesktopDir, flatpakDesktopDirs, snapBin, defaultBrowserID, homeDir
	t.Cleanup(func() {
		snapDesktopDir, flatpakDesktopDirs, snapBin, defaultBrowserID, homeDir = oldSnapDesk, oldFlat, oldBin, oldID, oldHome
	})
	snapDesktopDir = filepath.Join(root, "snapd")
	flatpakDesktopDirs = []string{filepath.Join(root, "flatpak"), ".local/share/flatpak/exports/share/applications"}
	snapBin = filepath.Join(root, "snapbin")
	defaultBrowserID = func() string { return defaultID }
	homeDir = func() (string, error) { return home, nil }
	return home, root
}

func touch(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestDefaultSnapBrowser(t *testing.T) {
	home, root := fakeSystem(t, "firefox_firefox.desktop")
	touch(t, filepath.Join(root, "snapd", "firefox_firefox.desktop"), "[Desktop Entry]\n")
	got := sandboxDir("")
	want := filepath.Join(home, "snap", "firefox", "common", "remohop")
	if got != want {
		t.Fatalf("sandboxDir = %q, want %q", got, want)
	}
}

func TestDefaultFlatpakBrowser(t *testing.T) {
	home, root := fakeSystem(t, "org.mozilla.firefox.desktop")
	touch(t, filepath.Join(root, "flatpak", "org.mozilla.firefox.desktop"), "[Desktop Entry]\n")
	if got := sandboxDir(""); got != "" {
		t.Fatalf("without ~/.var/app/<id> the Flatpak never ran: got %q, want the default", got)
	}
	if err := os.MkdirAll(filepath.Join(home, ".var", "app", "org.mozilla.firefox"), 0o700); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".var", "app", "org.mozilla.firefox", "cache", "remohop")
	if got := sandboxDir(""); got != want {
		t.Fatalf("sandboxDir = %q, want %q", got, want)
	}
}

func TestUserFlatpakBrowser(t *testing.T) {
	home, _ := fakeSystem(t, "org.chromium.Chromium.desktop")
	touch(t, filepath.Join(home, ".local/share/flatpak/exports/share/applications", "org.chromium.Chromium.desktop"), "x")
	if err := os.MkdirAll(filepath.Join(home, ".var", "app", "org.chromium.Chromium"), 0o700); err != nil {
		t.Fatal(err)
	}
	if got := sandboxDir(""); !strings.HasSuffix(got, filepath.Join("org.chromium.Chromium", "cache", "remohop")) {
		t.Fatalf("sandboxDir = %q", got)
	}
}

func TestDefaultPlainBrowser(t *testing.T) {
	fakeSystem(t, "google-chrome.desktop")
	if got := sandboxDir(""); got != "" {
		t.Fatalf("a browser that is neither a Snap nor a Flatpak: got %q", got)
	}
	fakeSystem(t, "")
	if got := sandboxDir(""); got != "" {
		t.Fatalf("no default browser known: got %q", got)
	}
	fakeSystem(t, "../evil.desktop")
	if got := sandboxDir(""); got != "" {
		t.Fatalf("a desktop ID with a path: got %q", got)
	}
}

func TestSnapProgram(t *testing.T) {
	home, root := fakeSystem(t, "")
	bin := filepath.Join(root, "snapbin")
	touch(t, filepath.Join(bin, "chromium"), "#!/bin/sh\n")
	want := filepath.Join(home, "snap", "chromium", "common", "remohop")
	if got := sandboxDir(filepath.Join(bin, "chromium")); got != want {
		t.Fatalf("/snap/bin/chromium: got %q, want %q", got, want)
	}
	// Ubuntu's /usr/bin/chromium-browser: a script that runs the Snap.
	wrapper := filepath.Join(root, "usr", "bin", "chromium-browser")
	touch(t, wrapper, "#!/bin/sh\nexec "+bin+"/chromium \"$@\"\n")
	if got := sandboxDir(wrapper); got != want {
		t.Fatalf("wrapper script: got %q, want %q", got, want)
	}
	// A link to /snap/bin/NAME.
	link := filepath.Join(root, "usr", "local", "bin", "chr")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(bin, "chromium"), link); err != nil {
		t.Fatal(err)
	}
	if got := sandboxDir(link); got != want {
		t.Fatalf("link: got %q, want %q", got, want)
	}
	// An ordinary browser binary.
	plain := filepath.Join(root, "opt", "google", "chrome")
	touch(t, plain, "\x7fELF...")
	if got := sandboxDir(plain); got != "" {
		t.Fatalf("plain program: got %q", got)
	}
}

func TestWriteIsPrivateAndSweepRemovesStale(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "handoff")
	path, remove, err := Write(dir, "web", Page("http://127.0.0.1:1/tok", "Opening…"))
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v, want 0600", fi.Mode().Perm())
	}
	di, _ := os.Stat(dir)
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v, want 0700", di.Mode().Perm())
	}
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), `location.replace("http://127.0.0.1:1/tok")`) {
		t.Fatalf("page: %s", b)
	}
	// Fresh files survive a sweep; old ones and only handoff files go.
	other := filepath.Join(dir, "notes.txt")
	touch(t, other, "keep")
	Sweep(dir)
	if _, err := os.Stat(path); err != nil {
		t.Fatal("a fresh handoff file was swept")
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(path, old, old)
	os.Chtimes(other, old, old)
	Sweep(dir)
	if _, err := os.Stat(path); err == nil {
		t.Fatal("a stale handoff file was kept")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("Sweep removed a file that is not a handoff file")
	}
	remove() // already gone: no panic
}
