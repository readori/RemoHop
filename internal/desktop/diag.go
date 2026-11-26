package desktop

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/redact"
)

// The diagnostic report: what support needs to follow a problem (versions,
// system, the app's state, its recent log) as one plain-text file the user
// saves and sends. It is written only when the user asks, to the Downloads
// folder, and goes through the same redaction as the mobile apps' report:
// no passwords, tokens or keys. Host IDs, names and addresses stay.

// diagLogLines bounds how much of desktop.log goes into a report.
const diagLogLines = 3000

// DiagnosticsReport builds the report's text.
func (a *App) DiagnosticsReport(ui UIInfo) string {
	st := a.State()
	un := a.UnattendedState()
	var b strings.Builder
	line := func(k, v string) { fmt.Fprintf(&b, "%-21s%s\n", k+":", v) }
	b.WriteString("RemoHop diagnostic report (desktop app)\n=======================================\n")
	line("Generated", time.Now().UTC().Format(time.RFC3339))
	line("App", cli.Version)
	line("System", osVersion()+" ("+runtime.GOOS+"/"+runtime.GOARCH+")")
	engine := "browser window"
	if ui.Native {
		engine = ui.Engine
	}
	line("Window", strings.TrimSpace(engine))
	line("Language", nonEmpty(st.Lang, "system"))
	server := st.Server.Mode
	if st.Server.Mode == serverCustom {
		server += " (" + st.Server.Addr + ")"
	}
	line("Rendezvous server", server)
	in := st.Incoming
	line("Incoming", fmt.Sprintf("on=%v phase=%s online=%v transport=%s", in.On, in.Phase, in.Online, nonEmpty(in.Transport, "-")))
	if in.Error != "" {
		line("Incoming error", in.ErrorCode+": "+in.Error)
	}
	line("Connected now", fmt.Sprint(len(in.Sessions)))
	line("Unattended", fmt.Sprintf("available=%v installed=%v running=%v", un.Available, un.Installed, un.Running))
	if un.Error != "" {
		line("Unattended error", un.Error)
	}
	line("Updates", fmt.Sprintf("auto=%v install=%s latest=%s", st.Update.Auto, st.Update.Method.Kind, nonEmpty(st.Update.Latest, "-")))
	if exe, err := os.Executable(); err == nil {
		line("Program", exe)
	}
	b.WriteString("\nSaved by the user from the app. Passwords, tokens and private keys are removed; host IDs, host names and IP addresses are kept.\n\n---- log ----\n")
	for _, l := range tail(filepath.Join(a.dir, "desktop.log"), diagLogLines) {
		b.WriteString(redact.Text(l))
		b.WriteByte('\n')
	}
	return redact.Text(b.String())
}

// ExportDiagnostics writes the report to the user's Downloads folder (or the
// temporary directory) and shows it in the file manager. It returns the
// file's path.
func (a *App) ExportDiagnostics(ui UIInfo) (string, error) {
	dir := downloadsDir()
	name := "remohop-diagnostics-" + time.Now().Format("20060102-150405") + ".txt"
	path := filepath.Join(dir, name)
	text := a.DiagnosticsReport(ui)
	if runtime.GOOS == "windows" {
		text = strings.ReplaceAll(text, "\n", "\r\n") // Notepad on older Windows
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		return "", err
	}
	a.log.Info("diagnostic report saved", "path", path)
	if err := revealFile(path); err != nil {
		a.log.Info("could not show the report in the file manager", "err", err)
	}
	return path, nil
}

func downloadsDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		d := filepath.Join(home, "Downloads")
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
		return home
	}
	return os.TempDir()
}

// tail returns the last n lines of a file (none if it cannot be read).
func tail(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > 2*n {
			lines = append(lines[:0], lines[len(lines)-n:]...)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
