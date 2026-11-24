package mobile

// Diagnostic log: an opt-in, local, size-capped log of what the core (its
// slog records, including the dialer's debug progress) and the app (native
// events it reports with DiagnosticsLog) did, for the user to send to
// support when something does not work.
//
//   - Off by default. SetDiagnosticsEnabled(true) starts it; turning it off
//     stops it and deletes the log, so "off" means nothing is kept.
//   - Kept in the app's private configuration directory
//     (<dir>/diagnostics/remohop.log), rotated at diagMaxFile into one
//     older file, so it never holds more than about 2*diagMaxFile.
//   - Never uploaded: DiagnosticsExport only writes a report file into a
//     directory the app names, which the app then offers in the system
//     share sheet or a mail composer when the user asks.
//   - Redacted twice: attributes whose names say they are secret are
//     replaced before formatting, and every line (and the report header)
//     goes through Redact, which removes gateway tokens in URLs, secret
//     query parameters, key=value secrets, bearer tokens, PEM private keys
//     and long opaque tokens. Host IDs, host names and IP addresses stay:
//     they are what support needs to follow a connection.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/signal"
	"github.com/readori/RemoHop/internal/web"
)

const (
	diagDirName  = "diagnostics"
	diagFlag     = "enabled"
	diagLogName  = "remohop.log"
	diagMaxFile  = 512 << 10 // per file; one rotated file is kept besides
	diagMaxLine  = 4 << 10   // longer lines are cut
	diagMaxEvent = 2 << 10   // DiagnosticsLog messages are cut to this
)

var diag struct {
	mu     sync.Mutex
	dir    string // the configuration directory the state below belongs to
	on     bool
	f      *os.File
	size   int64
	loaded bool
}

// diagPaths returns <dir>/diagnostics and the log file in it.
func diagPaths(d string) (string, string) {
	dd := filepath.Join(d, diagDirName)
	return dd, filepath.Join(dd, diagLogName)
}

// diagLoad (with diag.mu held) reads the persisted switch for the current
// configuration directory. It returns "" when Init has not been called.
//
// The directory comes from REMOHOP_CONFIG_DIR, which Init sets, rather than
// from the package's mu-guarded state: the logger calls this, and code that
// holds mu may log.
func diagLoad() string {
	d := os.Getenv("REMOHOP_CONFIG_DIR")
	if d == "" || !filepath.IsAbs(d) {
		return ""
	}
	if !diag.loaded || diag.dir != d {
		diagCloseLocked()
		diag.dir = d
		dd, _ := diagPaths(d)
		_, err := os.Stat(filepath.Join(dd, diagFlag))
		diag.on = err == nil
		diag.loaded = true
	}
	return d
}

func diagCloseLocked() {
	if diag.f != nil {
		diag.f.Close()
		diag.f = nil
	}
	diag.size = 0
}

// DiagnosticsEnabled reports whether the diagnostic log is recording.
func DiagnosticsEnabled() bool {
	diag.mu.Lock()
	defer diag.mu.Unlock()
	diagLoad()
	return diag.on
}

// SetDiagnosticsEnabled turns the diagnostic log on or off; the choice is
// kept across launches. Turning it off deletes what was recorded.
func SetDiagnosticsEnabled(on bool) error {
	if _, err := configDir(); err != nil {
		return err
	}
	diag.mu.Lock()
	d := diagLoad()
	dd, _ := diagPaths(d)
	if on {
		if err := os.MkdirAll(dd, 0o700); err != nil {
			diag.mu.Unlock()
			return err
		}
		if err := os.WriteFile(filepath.Join(dd, diagFlag), nil, 0o600); err != nil {
			diag.mu.Unlock()
			return err
		}
		was := diag.on
		diag.on = true
		diag.mu.Unlock()
		if !was {
			diagWrite(fmt.Sprintf("time=%s level=INFO msg=\"diagnostics on\" app=%q os=%s/%s", diagNow(), web.Version, runtime.GOOS, runtime.GOARCH))
		}
		return nil
	}
	diag.on = false
	diagCloseLocked()
	diag.mu.Unlock()
	if err := os.Remove(filepath.Join(dd, diagFlag)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return ClearDiagnostics()
}

// ClearDiagnostics deletes the recorded log (recording goes on if it is on).
func ClearDiagnostics() error {
	if _, err := configDir(); err != nil {
		return err
	}
	diag.mu.Lock()
	defer diag.mu.Unlock()
	d := diagLoad()
	diagCloseLocked()
	_, file := diagPaths(d)
	for _, p := range []string{file, file + ".1"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// DiagnosticsSize returns the size of the recorded log in bytes.
func DiagnosticsSize() int64 {
	diag.mu.Lock()
	defer diag.mu.Unlock()
	d := diagLoad()
	if d == "" {
		return 0
	}
	_, file := diagPaths(d)
	var n int64
	for _, p := range []string{file + ".1", file} {
		if fi, err := os.Stat(p); err == nil {
			n += fi.Size()
		}
	}
	return n
}

// DiagnosticsLog records an event of the app itself (source is a short
// word such as "ios", "android" or "web"), when the log is on. The message
// is redacted like everything else.
func DiagnosticsLog(source, message string) {
	if !DiagnosticsEnabled() {
		return
	}
	source = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return -1
	}, strings.ToLower(source))
	if source == "" {
		source = "app"
	}
	if len(message) > diagMaxEvent {
		message = message[:diagMaxEvent] + "…"
	}
	diagWrite(fmt.Sprintf("time=%s level=INFO source=%s msg=%s", diagNow(), source, strconv.Quote(oneLine(message))))
}

func diagNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func oneLine(s string) string {
	return strings.NewReplacer("\r\n", " ⏎ ", "\n", " ⏎ ", "\r", " ⏎ ").Replace(s)
}

// diagWrite appends one line (redacted, newline added) to the log, rotating
// it when it is full.
func diagWrite(line string) {
	line = Redact(strings.TrimRight(line, "\n"))
	if len(line) > diagMaxLine {
		line = line[:diagMaxLine] + "…"
	}
	diag.mu.Lock()
	defer diag.mu.Unlock()
	d := diagLoad()
	if d == "" || !diag.on {
		return
	}
	dd, file := diagPaths(d)
	if diag.f != nil && diag.size+int64(len(line))+1 > diagMaxFile {
		diagCloseLocked()
		os.Rename(file, file+".1")
	}
	if diag.f == nil {
		if err := os.MkdirAll(dd, 0o700); err != nil {
			return
		}
		f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return
		}
		diag.f, diag.size = f, fi.Size()
		if diag.size+int64(len(line))+1 > diagMaxFile {
			diagCloseLocked()
			f.Close()
			os.Rename(file, file+".1")
			if f, err = os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err != nil {
				return
			}
			diag.f = f
		}
	}
	n, _ := diag.f.WriteString(line + "\n")
	diag.size += int64(n)
}

// diagLines returns the recorded lines, oldest first.
func diagLines() []string {
	diag.mu.Lock()
	d := diagLoad()
	if diag.f != nil {
		diag.f.Sync()
	}
	diag.mu.Unlock()
	if d == "" {
		return nil
	}
	_, file := diagPaths(d)
	var out []string
	for _, p := range []string{file + ".1", file} {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64<<10), diagMaxLine*4)
		for sc.Scan() {
			// Redacted when written; again here in case the rules grew.
			out = append(out, Redact(sc.Text()))
		}
		f.Close()
	}
	return out
}

// ---- slog ----

// coreHandler is the core's logger: warnings and errors to stderr as
// before (quietLogger), and every record from debug level on into the
// diagnostic log while it is on.
type coreHandler struct {
	quiet slog.Handler
	file  slog.Handler
}

func newCoreHandler() slog.Handler {
	return &coreHandler{
		quiet: slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}),
		file: slog.NewTextHandler(diagSink{}, &slog.HandlerOptions{Level: slog.LevelDebug,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				if a.Key == slog.TimeKey && len(groups) == 0 {
					return slog.String(slog.TimeKey, a.Value.Time().UTC().Format("2006-01-02T15:04:05.000Z"))
				}
				if secretKey(a.Key) {
					return slog.String(a.Key, "[redacted]")
				}
				return a
			}}),
	}
}

func (h *coreHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.quiet.Enabled(ctx, l) || DiagnosticsEnabled()
}

func (h *coreHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.quiet.Enabled(ctx, r.Level) {
		h.quiet.Handle(ctx, r.Clone())
	}
	if DiagnosticsEnabled() {
		h.file.Handle(ctx, r)
	}
	return nil
}

func (h *coreHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &coreHandler{quiet: h.quiet.WithAttrs(as), file: h.file.WithAttrs(as)}
}

func (h *coreHandler) WithGroup(name string) slog.Handler {
	return &coreHandler{quiet: h.quiet.WithGroup(name), file: h.file.WithGroup(name)}
}

// diagSink receives one formatted record per Write from slog's text handler.
type diagSink struct{}

func (diagSink) Write(p []byte) (int, error) {
	diagWrite(string(bytes.TrimRight(p, "\n")))
	return len(p), nil
}

var _ io.Writer = diagSink{}

// ---- redaction ----

var secretWords = []string{"password", "passwd", "passphrase", "secret", "token", "ticket", "csrf", "cookie",
	"authorization", "privatekey", "private_key", "private-key", "otp"}

// secretKey reports whether an attribute named k holds a secret.
func secretKey(k string) bool {
	k = strings.ToLower(k)
	if k == "pw" || k == "pass" || k == "sid" || k == "t" {
		return true
	}
	for _, w := range secretWords {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

var redactions = []struct {
	re   *regexp.Regexp
	with string
}{
	// PEM private keys (and anything armoured as one).
	{regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?(-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`), "[private key redacted]"},
	// The gateway's access token is the first path element of its URL.
	{regexp.MustCompile(`(?i)(\b(?:https?|wss?)://[^/\s"'<>]+/)[A-Za-z0-9_~-]{12,}`), "${1}[token]"},
	// Secret query parameters.
	{regexp.MustCompile(`(?i)([?&](?:t|token|ticket|password|pass|pw|secret|csrf|sid|key|otp)=)[^&\s"'<>]+`), "${1}[redacted]"},
	// Authorization headers.
	{regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=-]{8,}`), "${1} [redacted]"},
	// key=value and "key": "value" pairs whose key names a secret.
	{regexp.MustCompile(`(?i)("?\b[a-z0-9_.-]*(?:password|passwd|passphrase|secret|token|ticket|csrf|cookie|private[_-]?key|otp)[a-z0-9_.-]*"?\s*[=:]\s*)("(?:[^"\\]|\\.)*"|'[^']*'|[^\s,;&}\]]+)`), "${1}[redacted]"},
	{regexp.MustCompile(`(?i)(\b(?:pw|pass|sid|x-remohop-session|x-remohop-csrf)\s*[=:]\s*)("(?:[^"\\]|\\.)*"|[^\s,;&}\]]+)`), "${1}[redacted]"},
	// Long opaque strings (tokens, keys) that slipped through: 32 or more
	// base64/hex characters in one run. Fingerprints (groups of four hex
	// digits with colons), IDs and addresses are shorter runs.
	{regexp.MustCompile(`[A-Za-z0-9+/_-]{32,}={0,2}`), "[redacted]"},
}

// Redact removes secrets from s: gateway tokens in URLs, secret query
// parameters and key=value pairs, authorization headers, PEM private keys
// and long opaque tokens. Host IDs, host names and IP addresses are kept.
func Redact(s string) string {
	for _, r := range redactions {
		s = r.re.ReplaceAllString(s, r.with)
	}
	return s
}

// ---- export ----

// diagAppInfo is what the app tells DiagnosticsExport about itself.
type diagAppInfo struct {
	App      string `json:"app_version"`
	Build    string `json:"build"`
	Platform string `json:"platform"`
	OS       string `json:"os"`
	Device   string `json:"device"`
	Language string `json:"language"`
}

// diagReport is the exported report (the JSON form; the text form holds
// the same fields).
type diagReport struct {
	Generated string            `json:"generated"`
	App       diagAppInfo       `json:"app"`
	Core      map[string]string `json:"core"`
	Settings  map[string]string `json:"settings"`
	Note      string            `json:"note"`
	Log       []string          `json:"log"`
}

const diagNote = "Recorded on this device only and shared by the user. Passwords, tokens and private keys are removed; host IDs, host names and IP addresses are kept."

// CoreVersion describes the Go core: the version stamped at build time
// (-ldflags "-X github.com/readori/RemoHop/internal/cli.Version=...", see
// mobile/android/build.sh and mobile/ios/build-xcframework.sh), or else its
// module version or revision, and the Go release it was built with.
func CoreVersion() string {
	v := "dev"
	if cli.Version != "" && cli.Version != "dev" {
		v = cli.Version
	}
	extra := []string{runtime.Version()}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range append([]*debug.Module{&bi.Main}, bi.Deps...) {
			// The apps' module requires this one as v0.0.0 with a replace:
			// that says nothing.
			if v == "dev" && dep.Path == "github.com/readori/RemoHop" && dep.Version != "" && dep.Version != "(devel)" && dep.Version != "v0.0.0" {
				v = dep.Version
			}
		}
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" && len(s.Value) >= 7 && !strings.Contains(v, s.Value[:7]) {
				extra = append(extra, "rev "+s.Value[:7])
			}
		}
	}
	return v + " (" + strings.Join(extra, ", ") + ")"
}

func buildReport(appInfoJSON string) diagReport {
	var info diagAppInfo
	json.Unmarshal([]byte(appInfoJSON), &info)
	for _, p := range []*string{&info.App, &info.Build, &info.Platform, &info.OS, &info.Device, &info.Language} {
		*p = Redact(oneLine(strings.TrimSpace(*p)))
		if len(*p) > 200 {
			*p = (*p)[:200]
		}
	}
	rep := diagReport{
		Generated: time.Now().UTC().Format(time.RFC3339),
		App:       info,
		Core:      map[string]string{"version": CoreVersion(), "arch": runtime.GOOS + "/" + runtime.GOARCH},
		Settings:  map[string]string{},
		Note:      diagNote,
		Log:       diagLines(),
	}
	if rep.Log == nil {
		rep.Log = []string{}
	}
	if d, err := configDir(); err == nil {
		all, _ := config.AllIn(d)
		rv := all[signal.SettingAddr]
		if rv == "" {
			rv = "(not set)"
		}
		rep.Settings["rendezvous_server"] = Redact(rv)
		rep.Settings["server_key_set"] = yesNo(all[signal.SettingPin] != "")
		relay := all[signal.SettingRelay]
		if relay == "" {
			relay = "on"
		}
		rep.Settings["relay"] = relay
		rep.Settings["device_name"] = Redact(all[hostd.SettingName])
		rep.Settings["identity"] = fingerprint(d)
		if devs, err := DevicesJSON(false); err == nil {
			var list []json.RawMessage
			json.Unmarshal([]byte(devs), &list)
			rep.Settings["saved_hosts"] = fmt.Sprint(len(list))
		}
		rep.Settings["diagnostics"] = yesNo(DiagnosticsEnabled())
	}
	return rep
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// DiagnosticsReport returns the report as text ("txt") or JSON ("json"):
// a header (app and core versions, OS and device, language, connection
// settings without secrets) and the recorded log lines.
func DiagnosticsReport(appInfoJSON, format string) (string, error) {
	if _, err := configDir(); err != nil {
		return "", err
	}
	rep := buildReport(appInfoJSON)
	switch format {
	case "json":
		b, err := json.MarshalIndent(rep, "", "  ")
		return string(b) + "\n", err
	case "txt", "":
	default:
		return "", fmt.Errorf("mobile: unknown report format %q", format)
	}
	var b strings.Builder
	row := func(k, v string) {
		if v != "" {
			fmt.Fprintf(&b, "%-20s %s\n", k+":", v)
		}
	}
	b.WriteString("RemoHop diagnostic report\n")
	b.WriteString("=========================\n")
	row("Generated", rep.Generated)
	app := rep.App.App
	if rep.App.Build != "" {
		app += " (build " + rep.App.Build + ")"
	}
	row("App", app)
	row("Platform", strings.TrimSpace(rep.App.Platform+" "+rep.App.OS))
	row("Device", rep.App.Device)
	row("Language", rep.App.Language)
	row("Core", rep.Core["version"]+" "+rep.Core["arch"])
	for _, k := range [][2]string{{"rendezvous_server", "Rendezvous server"}, {"server_key_set", "Server key set"}, {"relay", "Relay"},
		{"device_name", "Device name"}, {"identity", "Identity"}, {"saved_hosts", "Saved hosts"}, {"diagnostics", "Diagnostics"}} {
		row(k[1], rep.Settings[k[0]])
	}
	row("Log lines", fmt.Sprint(len(rep.Log)))
	b.WriteString("\n" + rep.Note + "\n\n")
	b.WriteString("---- log ----\n")
	for _, l := range rep.Log {
		b.WriteString(l + "\n")
	}
	return b.String(), nil
}

// DiagnosticsExport writes the report (see DiagnosticsReport) to a new file
// in outDir (the app's cache directory, from which it shares the file) and
// returns the file's path. Older reports in outDir are removed first.
func DiagnosticsExport(appInfoJSON, format, outDir string) (string, error) {
	if outDir == "" || !filepath.IsAbs(outDir) {
		return "", errors.New("mobile: the export directory must be an absolute path")
	}
	s, err := DiagnosticsReport(appInfoJSON, format)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return "", err
	}
	if old, _ := filepath.Glob(filepath.Join(outDir, "remohop-diagnostics-*")); len(old) > 0 {
		for _, p := range old {
			os.Remove(p)
		}
	}
	ext := "txt"
	if format == "json" {
		ext = "json"
	}
	p := filepath.Join(outDir, "remohop-diagnostics-"+time.Now().UTC().Format("20060102-150405")+"."+ext)
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		return "", err
	}
	return p, nil
}
