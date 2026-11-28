package ffmpeg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/readori/RemoHop/internal/cli"
)

// fakeService is a Target for the Windows service, in a directory of its
// own, with an environment of its own (env), recording the calls of its
// Secure.
type fakeService struct {
	Target
	env     map[string]string
	secured []string
}

func newFakeService(t *testing.T) *fakeService {
	f := &fakeService{env: map[string]string{}}
	f.Target = Target{Name: "the test service", ConfigDir: t.TempDir(),
		Getenv: func(k string) string { return f.env[k] },
		Secure: func(dir string, installed bool) (string, error) {
			f.secured = append(f.secured, map[bool]string{false: "before", true: "after"}[installed]+" "+dir)
			return "", os.MkdirAll(dir, 0o700)
		}}
	return f
}

func setSetting(t *testing.T, dir, value string) {
	t.Helper()
	b, _ := json.Marshal(map[string]string{SettingDownload: value})
	if err := os.WriteFile(filepath.Join(dir, "config.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestFindFor: the service's ffmpeg is found with its environment and in
// its directory, not this process's: its REMOHOP_FFMPEG, then its PATH
// (absolute entries only), then its copy.
func TestFindFor(t *testing.T) {
	isolate(t)
	// What this process has does not count for the service.
	own, _ := path()
	writeExe(t, own)
	mine := t.TempDir()
	writeExe(t, filepath.Join(mine, exeName()))
	t.Setenv("PATH", mine)
	t.Setenv(PathEnv, filepath.Join(mine, exeName()))

	svc := newFakeService(t)
	if _, err := FindFor(svc.Target); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the service has nothing: %v", err)
	}
	copyPath := filepath.Join(svc.ConfigDir, "ffmpeg", exeName())
	writeExe(t, copyPath)
	if f, err := FindFor(svc.Target); err != nil || f != (Found{Path: copyPath, Kind: KindRemoHop}) {
		t.Errorf("its copy: %+v %v", f, err)
	}
	sys := t.TempDir()
	writeExe(t, filepath.Join(sys, exeName()))
	svc.env["PATH"] = strings.Join([]string{"", "relative", sys}, string(filepath.ListSeparator))
	if f, err := FindFor(svc.Target); err != nil || f != (Found{Path: filepath.Join(sys, exeName()), Kind: KindSystem}) {
		t.Errorf("its PATH: %+v %v", f, err)
	}
	svc.env[PathEnv] = `C:\tools\ffmpeg.exe`
	if f, err := FindFor(svc.Target); err != nil || f != (Found{Path: `C:\tools\ffmpeg.exe`, Kind: KindEnv}) {
		t.Errorf("its REMOHOP_FFMPEG: %+v %v", f, err)
	}

	if runtime.GOOS != "windows" {
		// A file that is not executable is not a program on PATH.
		plain := t.TempDir()
		os.WriteFile(filepath.Join(plain, exeName()), []byte("x"), 0o644)
		if p, ok := lookPathIn(plain); ok {
			t.Errorf("a non-executable file was taken: %s", p)
		}
	}
}

// TestDownloadDisabledFor: every switch counts for the service: this
// process's environment and this user's setting, and the service's
// environment and setting (the machine's).
func TestDownloadDisabledFor(t *testing.T) {
	userDir := isolate(t)
	svc := newFakeService(t)
	if off, by := DownloadDisabledFor(svc.Target); off {
		t.Fatalf("nothing switched off, yet off by %s", by)
	}
	setSetting(t, userDir, "off")
	if off, by := DownloadDisabledFor(svc.Target); !off || !strings.Contains(by, "setting") || strings.Contains(by, svc.Name) {
		t.Errorf("this user's setting: %v %q", off, by)
	}
	os.Remove(filepath.Join(userDir, "config.json"))

	setSetting(t, svc.ConfigDir, "off")
	if off, by := DownloadDisabledFor(svc.Target); !off || !strings.Contains(by, "setting "+SettingDownload+" (off) of "+svc.Name) {
		t.Errorf("the service's setting: %v %q", off, by)
	}
	// The environment wins over this user's setting, not over the
	// machine's.
	t.Setenv(DownloadEnv, "on")
	if off, _ := DownloadDisabledFor(svc.Target); !off {
		t.Error("REMOHOP_FFMPEG_DOWNLOAD=on here overrode the service's setting")
	}
	os.Remove(filepath.Join(svc.ConfigDir, "config.json"))
	if off, by := DownloadDisabledFor(svc.Target); off {
		t.Errorf("all on, yet off by %s", by)
	}
	svc.env[DownloadEnv] = "Off"
	if off, by := DownloadDisabledFor(svc.Target); !off || !strings.Contains(by, "environment of "+svc.Name) {
		t.Errorf("the service's environment: %v %q", off, by)
	}
	svc.env[DownloadEnv] = ""
	t.Setenv(DownloadEnv, "0")
	if off, by := DownloadDisabledFor(svc.Target); !off || by != DownloadEnv+"=0" {
		t.Errorf("this process's environment: %v %q", off, by)
	}
}

// TestPlanFor: install for the service only when it finds no ffmpeg at
// all, there is a build, and downloads are not switched off.
func TestPlanFor(t *testing.T) {
	isolate(t)
	svc := newFakeService(t)
	if p, err := PlanFor(svc.Target); err != nil || !p.Install || p.Found != nil || p.Why != "" {
		t.Errorf("nothing there: %+v %v", p, err)
	}
	setSetting(t, svc.ConfigDir, "off")
	if p, err := PlanFor(svc.Target); err != nil || p.Install || !strings.Contains(p.Why, "switched off") {
		t.Errorf("switched off: %+v %v", p, err)
	}
	pinned = func() (Build, bool) { return Build{}, false }
	if p, err := PlanFor(svc.Target); err != nil || p.Install || p.Why != ErrUnsupported.Error() {
		t.Errorf("no build: %+v %v", p, err)
	}
	sys := t.TempDir()
	writeExe(t, filepath.Join(sys, exeName()))
	svc.env["PATH"] = sys
	if p, err := PlanFor(svc.Target); err != nil || p.Install || p.Found == nil || p.Found.Kind != KindSystem {
		t.Errorf("on its PATH: %+v %v", p, err)
	}
}

// TestInstallFor: the service's copy is the same verified download, in the
// service's directory, secured before it is looked at and after the
// install; a service whose setting switches downloads off gets nothing.
func TestInstallFor(t *testing.T) {
	userDir := isolate(t)
	s := newServer(t)
	member := "ffmpeg-test/bin/" + exeName()
	s.files["/a.zip"] = makeZip(t, entry{name: member, data: program})
	b := buildOf(s.files["/a.zip"], member, len(program), s.URL+"/a.zip")
	svc := newFakeService(t)
	dir := filepath.Join(svc.ConfigDir, "ffmpeg")

	plan, _, err := PrepareFor(svc.Target)
	if err != nil || !plan.Install {
		t.Fatalf("plan: %+v %v", plan, err)
	}
	o := testOptions(s, b, "")
	f, err := InstallFor(context.Background(), svc.Target, o)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Found{Path: filepath.Join(dir, exeName()), Kind: KindRemoHop}); f != want {
		t.Errorf("found %+v, want %+v", f, want)
	}
	if got := strings.Join(svc.secured, "; "); got != "before "+dir+"; after "+dir {
		t.Errorf("Secure calls: %s", got)
	}
	if data, err := os.ReadFile(f.Path); err != nil || !bytes.Equal(data, program) {
		t.Errorf("installed: %q %v", data, err)
	}
	if m, ok := ManifestFor(svc.Target); !ok || m.SHA256 != b.SHA256 {
		t.Errorf("manifest: %+v %v", m, ok)
	}
	// This user's copy is not touched.
	if l, _ := os.ReadDir(filepath.Join(userDir, "ffmpeg")); len(l) != 0 {
		t.Errorf("this user's directory got %v", l)
	}

	other := newFakeService(t)
	setSetting(t, other.ConfigDir, "off")
	hits := s.hits.Load()
	if _, err := InstallFor(context.Background(), other.Target, testOptions(s, b, "")); err == nil || !strings.Contains(err.Error(), "switched off") || s.hits.Load() != hits {
		t.Errorf("switched off by the service's setting: %v, %d requests", err, s.hits.Load()-hits)
	}
}

// TestCommandService: `ffmpeg install --service` and the status's service
// line.
func TestCommandService(t *testing.T) {
	isolate(t)
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := cli.Main(context.Background(), args, strings.NewReader(""), &out, &errb)
		return code, out.String(), errb.String()
	}
	old := Service
	t.Cleanup(func() { Service = old })

	Service = nil
	if code, _, errs := run("ffmpeg", "install", "--service"); code != cli.ExitError || !strings.Contains(errs, "only the Windows service") {
		t.Errorf("no service hook: %d %q", code, errs)
	}
	if code, _, _ := run("ffmpeg", "remove", "--service"); code != cli.ExitUsage {
		t.Errorf("remove --service: %d", code)
	}
	Service = func(context.Context) (Target, bool, error) { return Target{}, false, nil }
	if code, _, errs := run("ffmpeg", "install", "--service"); code != cli.ExitError || !strings.Contains(errs, "not installed with unattended access") {
		t.Errorf("no service: %d %q", code, errs)
	}

	svc := newFakeService(t)
	Service = func(context.Context) (Target, bool, error) { return svc.Target, true, nil }
	if code, out, _ := run("ffmpeg"); code != 0 || !strings.Contains(out, "service:  not found: the test service cannot share the screen") {
		t.Errorf("status without the service's ffmpeg: %d %q", code, out)
	}
	sys := t.TempDir()
	writeExe(t, filepath.Join(sys, exeName()))
	svc.env["PATH"] = sys
	code, out, _ := run("ffmpeg", "install", "--service")
	if code != 0 || !strings.Contains(out, "nothing to install") || len(svc.secured) != 1 {
		t.Errorf("install --service when it has one: %d %q %v", code, out, svc.secured)
	}
	if code, out, _ := run("ffmpeg", "status", "--json"); code != 0 || !strings.Contains(out, `"service"`) || !strings.Contains(out, sys) {
		t.Errorf("status --json: %d %q", code, out)
	}
	svc.env["PATH"] = ""
	setSetting(t, svc.ConfigDir, "off")
	if code, _, errs := run("ffmpeg", "install", "--service"); code != cli.ExitError || !strings.Contains(errs, "switched off") {
		t.Errorf("switched off for the service: %d %q", code, errs)
	}
}
