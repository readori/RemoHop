package mobile

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/readori/RemoHop/internal/cli"
)

func TestRedact(t *testing.T) {
	cases := []struct{ in, gone, kept string }{
		{"GET http://127.0.0.1:41234/AbCdEf0123456789xyzQ/api/v1/session", "AbCdEf0123456789xyzQ", "127.0.0.1:41234/"},
		{"ws://127.0.0.1:5000/Zx9_-abcdefghijklmn/api/v1/desktop/ws?t=tick3tvalue&c=1", "Zx9_-abcdefghijklmn", "c=1"},
		{"download ?path=/x&t=0123456789abcdef", "0123456789abcdef", "path=/x"},
		{"login password=hunter2 target=123456789", "hunter2", "target=123456789"},
		{`{"password":"s3cr3t pass","target":"lab"}`, "s3cr3t", `"target":"lab"`},
		{"pairing_token=abcDEF123 host=10.0.0.5:21118", "abcDEF123", "10.0.0.5:21118"},
		{"Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload", "eyJhbGciOiJIUzI1NiJ9", "Authorization"},
		{"X-RemoHop-Session: 7f3a9c second", "7f3a9c", "second"},
		{"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----", "b3BlbnNzaC1rZXktdjE", "[private key redacted]"},
		{"tok 4f2c9a1b7e6d5c4b3a291807f6e5d4c3b2a19087 end", "4f2c9a1b7e6d5c4b3a291807f6e5d4c3b2a19087", "end"},
		{"pw: abc123 ok", "abc123", "ok"},
		{"host_identity=8ec2:4de8:0bd0:ccca:2efb id=123456789 addr=192.168.1.20:21118 [2001:db8::1]:21118", "", "8ec2:4de8:0bd0:ccca:2efb id=123456789 addr=192.168.1.20:21118 [2001:db8::1]:21118"},
	}
	for _, c := range cases {
		got := Redact(c.in)
		if c.gone != "" && strings.Contains(got, c.gone) {
			t.Errorf("Redact(%q) = %q: still holds %q", c.in, got, c.gone)
		}
		if !strings.Contains(got, c.kept) {
			t.Errorf("Redact(%q) = %q: lost %q", c.in, got, c.kept)
		}
	}
}

func TestDiagnosticsOffByDefault(t *testing.T) {
	d := initDir(t)
	if DiagnosticsEnabled() {
		t.Fatal("diagnostics on in a fresh directory")
	}
	DiagnosticsLog("ios", "should not be kept")
	quietLogger().Info("nor this")
	if n := DiagnosticsSize(); n != 0 {
		t.Fatalf("log has %d bytes while off", n)
	}
	if _, err := os.Stat(filepath.Join(d, diagDirName, diagLogName)); !os.IsNotExist(err) {
		t.Fatalf("log file exists while off: %v", err)
	}
}

func TestDiagnosticsRecordExportClear(t *testing.T) {
	d := initDir(t)
	if err := SetSetting("signal", "rv.example.net:21116"); err != nil {
		t.Fatal(err)
	}
	if err := SetDiagnosticsEnabled(true); err != nil {
		t.Fatal(err)
	}
	if !DiagnosticsEnabled() {
		t.Fatal("not enabled")
	}
	log := quietLogger()
	log.Debug("dial progress", "target", "123456789", "addr", "192.168.1.20:21118")
	log.Info("gateway", "url", "http://127.0.0.1:41234/AbCdEf0123456789xyzQ/", "password", "hunter2")
	log.With("token", "abcdef").Warn("pair", "host", "lab")
	DiagnosticsLog("Android!", "page error\nat http://127.0.0.1:41234/AbCdEf0123456789xyzQ/assets/app.js password=x")

	fi, err := os.Stat(filepath.Join(d, diagDirName, diagLogName))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("log mode %v", fi.Mode().Perm())
	}
	if DiagnosticsSize() == 0 {
		t.Fatal("nothing recorded")
	}

	info := `{"app_version":"1.2.3","build":"45","platform":"iOS","os":"18.1","device":"iPhone15,2","language":"zh"}`
	txt, err := DiagnosticsReport(info, "txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"RemoHop diagnostic report", "1.2.3 (build 45)", "iOS 18.1", "iPhone15,2", "Language:", "zh",
		"rv.example.net:21116", "Relay:", "dial progress", "target=123456789", "192.168.1.20:21118", `source=android msg="page error ⏎ at`,
		"---- log ----", "host=lab", "diagnostics on"} {
		if !strings.Contains(txt, want) {
			t.Errorf("text report lacks %q:\n%s", want, txt)
		}
	}
	for _, bad := range []string{"AbCdEf0123456789xyzQ", "hunter2", "abcdef", "password=x"} {
		if strings.Contains(txt, bad) {
			t.Errorf("text report holds the secret %q:\n%s", bad, txt)
		}
	}

	js, err := DiagnosticsReport(info, "json")
	if err != nil {
		t.Fatal(err)
	}
	var rep diagReport
	if err := json.Unmarshal([]byte(js), &rep); err != nil {
		t.Fatal(err)
	}
	if rep.App.App != "1.2.3" || rep.Settings["rendezvous_server"] != "rv.example.net:21116" || len(rep.Log) < 4 || rep.Core["version"] == "" {
		t.Errorf("json report: %+v", rep)
	}
	if strings.Contains(js, "hunter2") || strings.Contains(js, "AbCdEf0123456789xyzQ") {
		t.Errorf("json report holds a secret")
	}
	if _, err := DiagnosticsReport(info, "pdf"); err == nil {
		t.Error("unknown format accepted")
	}

	out := filepath.Join(t.TempDir(), "share")
	p1, err := DiagnosticsExport(info, "txt", out)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := DiagnosticsExport(info, "json", out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(p1, ".txt") || !strings.HasSuffix(p2, ".json") || filepath.Dir(p2) != out {
		t.Errorf("export paths %q %q", p1, p2)
	}
	if left, _ := filepath.Glob(filepath.Join(out, "remohop-diagnostics-*")); len(left) != 1 {
		t.Errorf("old exports not removed: %v", left)
	}
	if _, err := DiagnosticsExport(info, "txt", "relative/dir"); err == nil {
		t.Error("relative export directory accepted")
	}

	if err := ClearDiagnostics(); err != nil {
		t.Fatal(err)
	}
	if n := DiagnosticsSize(); n != 0 {
		t.Errorf("%d bytes after clear", n)
	}
	if !DiagnosticsEnabled() {
		t.Error("clear turned recording off")
	}
	DiagnosticsLog("ios", "after clear")
	if DiagnosticsSize() == 0 {
		t.Error("not recording after clear")
	}

	// Off deletes the log, and stays off for the next launch.
	if err := SetDiagnosticsEnabled(false); err != nil {
		t.Fatal(err)
	}
	if DiagnosticsSize() != 0 || DiagnosticsEnabled() {
		t.Error("off kept the log or the switch")
	}
}

func TestDiagnosticsPersistAndRotate(t *testing.T) {
	d := initDir(t)
	if err := SetDiagnosticsEnabled(true); err != nil {
		t.Fatal(err)
	}
	// A new process (or Init again) finds the switch on.
	diag.mu.Lock()
	diag.loaded = false
	diagCloseLocked()
	diag.mu.Unlock()
	if !DiagnosticsEnabled() {
		t.Fatal("the switch did not persist")
	}
	line := strings.Repeat("x ", 900)
	for i := 0; i < 1500; i++ {
		DiagnosticsLog("ios", line)
	}
	if n := DiagnosticsSize(); n > 2*diagMaxFile+diagMaxLine {
		t.Errorf("log grew to %d bytes", n)
	}
	if _, err := os.Stat(filepath.Join(d, diagDirName, diagLogName+".1")); err != nil {
		t.Errorf("no rotated file: %v", err)
	}
	if !quietLogger().Enabled(context.Background(), slog.LevelDebug) {
		t.Error("debug records are dropped while diagnostics are on")
	}
	SetDiagnosticsEnabled(false)
	if quietLogger().Enabled(context.Background(), slog.LevelInfo) {
		t.Error("info records pass while diagnostics are off")
	}
}

func TestCoreVersion(t *testing.T) {
	if v := CoreVersion(); !strings.Contains(v, "go1.") || strings.HasPrefix(v, "v0.0.0") {
		t.Errorf("CoreVersion() = %q", v)
	}
	// The build scripts stamp the app's version and commit.
	old := cli.Version
	t.Cleanup(func() { cli.Version = old })
	cli.Version = "1.0.0+38bab3e"
	if v := CoreVersion(); !strings.HasPrefix(v, "1.0.0+38bab3e (go1.") {
		t.Errorf("stamped CoreVersion() = %q", v)
	}
}
