package desktop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The report has the app's facts and the end of its log, without secrets.
func TestDiagnosticsReport(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", dir)
	var log strings.Builder
	for i := 0; i < diagLogLines+50; i++ {
		log.WriteString("time=x level=INFO msg=filler\n")
	}
	log.WriteString(`time=y level=WARN msg="unattended access could not be turned on" err="access denied" password=hunter2-secret token=abcdefghijklmnopqrstuvwxyz0123456789` + "\n")
	if err := os.WriteFile(filepath.Join(dir, "desktop.log"), []byte(log.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := NewApp(Config{NoUpdateCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	r := a.DiagnosticsReport(UIInfo{Native: true, Engine: "WebView2"})
	for _, want := range []string{"RemoHop diagnostic report", "App:", "System:", "WebView2", "Rendezvous server:", "Unattended:", "---- log ----", "access denied"} {
		if !strings.Contains(r, want) {
			t.Errorf("report lacks %q", want)
		}
	}
	for _, gone := range []string{"hunter2-secret", "abcdefghijklmnopqrstuvwxyz0123456789"} {
		if strings.Contains(r, gone) {
			t.Errorf("report holds a secret: %q", gone)
		}
	}
	if n := strings.Count(r, "msg=filler"); n > diagLogLines {
		t.Errorf("%d log lines, more than %d", n, diagLogLines)
	}
}
