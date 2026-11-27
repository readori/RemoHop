package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		latest, current string
		want            bool
	}{
		{"v1.0.2", "v1.0.1", true},
		{"v1.1.0", "v1.0.9", true},
		{"v2.0.0", "v1.9.9", true},
		{"v1.0.10", "v1.0.9", true},
		{"v1.0.1", "v1.0.1", false},
		{"v1.0.1", "v1.0.2", false},
		{"v1.0.3", "v1.0.3-rc.1", true},
		{"v1.0.2", "v1.0.3-rc.1", false},
		{"v1.0.2", "dev", false},
		{"v1.0.2", "", false},
		{"v1.0.2", "v0.0.0-test.1", true},
		{"garbage", "v1.0.1", false},
		// Builds from source (git describe) are never out of date.
		{"v1.0.7", "v1.0.7-3-gabc1234", false},
		{"v1.0.7", "v1.0.6-12-g0123456789ab-dirty", false},
		{"v1.0.7", "v1.0.6-dirty", false},
		{"v1.0.7", "v1.0.7-rc.1-2-gdeadbee", false},
		{"v1.0.7", "abc1234", false},
		{"v1.0.7", "v1.0.7-rc.2", true},
	} {
		if got := Newer(c.latest, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}

func TestParse(t *testing.T) {
	r, err := Parse([]byte(`{"version":"v1.0.2","extra":1}`))
	if err != nil || r.Version != "v1.0.2" {
		t.Fatalf("Parse = %+v, %v", r, err)
	}
	if r.NotesURL() != "https://github.com/readori/RemoHop/releases/tag/v1.0.2" {
		t.Errorf("NotesURL = %s", r.NotesURL())
	}
	for _, bad := range []string{`{}`, `{"version":"1.0.2"}`, `{"version":"v1.0.2-rc.1"}`, `{"version":"v1.0.2/../../x"}`, `not json`, `{"version":"v01.0.2"}`} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse(%s) accepted", bad)
		}
	}
}

func TestFetch(t *testing.T) {
	var ua, cookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua, cookie = r.UserAgent(), r.Header.Get("Cookie")
		switch r.URL.Path {
		case "/version.json":
			w.Write([]byte(`{"version":"v9.8.7"}`))
		case "/big":
			w.Write([]byte(`{"version":"v1.0.0","pad":"` + strings.Repeat("x", maxBody) + `"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	r, err := Fetch(context.Background(), srv.Client(), srv.URL+"/version.json")
	if err != nil || r.Version != "v9.8.7" {
		t.Fatalf("Fetch = %+v, %v", r, err)
	}
	if ua != "RemoHop" || cookie != "" {
		t.Errorf("request identifies itself: User-Agent %q, Cookie %q", ua, cookie)
	}
	if _, err := Fetch(context.Background(), srv.Client(), srv.URL+"/missing"); err == nil {
		t.Error("a 404 was accepted")
	}
	if _, err := Fetch(context.Background(), srv.Client(), srv.URL+"/big"); err == nil {
		t.Error("an oversized file was accepted")
	}
}

func TestDetect(t *testing.T) {
	exists := func(paths ...string) func(string) bool {
		return func(p string) bool {
			for _, x := range paths {
				if x == p {
					return true
				}
			}
			return false
		}
	}
	env := func(k, v string) func(string) string {
		return func(n string) string {
			if n == k {
				return v
			}
			return ""
		}
	}
	for _, c := range []struct {
		name string
		e    Env
		want string
	}{
		{"zip on windows", Env{GOOS: "windows", Exe: `C:\Users\a\Downloads\RemoHop-windows-amd64\RemoHop.exe`}, KindDownload},
		{"install.ps1", Env{GOOS: "windows", Exe: `C:\Users\a\AppData\Local\Programs\RemoHop\RemoHop.exe`}, KindDownload},
		{"scoop", Env{GOOS: "windows", Exe: `C:\Users\a\scoop\apps\remohop\1.0.2\remohop.exe`}, KindScoop},
		{"winget", Env{GOOS: "windows", Exe: `C:\Users\a\AppData\Local\Microsoft\WinGet\Packages\RemoHop.RemoHop_Microsoft.Winget.Source_8wekyb3d8bbwe\remohop.exe`}, KindWinget},
		{"dmg", Env{GOOS: "darwin", Exe: "/Applications/RemoHop.app/Contents/MacOS/RemoHop"}, KindDownload},
		{"cask", Env{GOOS: "darwin", Exe: "/Applications/RemoHop.app/Contents/MacOS/RemoHop", Exists: exists("/opt/homebrew/Caskroom/remohop-quick")}, KindHomebrew},
		{"formula", Env{GOOS: "darwin", Exe: "/opt/homebrew/Cellar/remohop/1.0.2/bin/remohop"}, KindHomebrew},
		{"linuxbrew", Env{GOOS: "linux", Exe: "/home/linuxbrew/.linuxbrew/Cellar/remohop/1.0.2/bin/remohop"}, KindHomebrew},
		{"deb", Env{GOOS: "linux", Exe: "/usr/bin/remohop"}, KindPackage},
		{"tarball", Env{GOOS: "linux", Exe: "/home/a/remohop-app"}, KindDownload},
		{"install.sh", Env{GOOS: "linux", Exe: "/usr/local/bin/remohop"}, KindDownload},
		{"snap", Env{GOOS: "linux", Exe: "/snap/remohop/12/bin/remohop", Getenv: env("SNAP", "/snap/remohop/12")}, KindSnap},
		{"nix", Env{GOOS: "linux", Exe: "/nix/store/abc-remohop-1.0.2/bin/remohop"}, KindNix},
	} {
		m := Detect(c.e)
		if m.Kind != c.want {
			t.Errorf("%s: Detect = %+v, want %s", c.name, m, c.want)
		}
		if (m.Kind == KindHomebrew || m.Kind == KindScoop || m.Kind == KindWinget || m.Kind == KindSnap) && m.Command == "" {
			t.Errorf("%s: no upgrade command", c.name)
		}
	}
}
