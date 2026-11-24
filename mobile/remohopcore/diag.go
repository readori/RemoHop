package remohopcore

import "github.com/readori/RemoHop/internal/mobile"

// The diagnostic log (see internal/mobile/diag.go): off by default, local,
// size-capped and redacted; the app shares a report only when the user asks.

// DiagnosticsEnabled reports whether the diagnostic log is recording.
func DiagnosticsEnabled() bool { return mobile.DiagnosticsEnabled() }

// SetDiagnosticsEnabled turns the log on or off (off deletes it).
func SetDiagnosticsEnabled(on bool) error { return mobile.SetDiagnosticsEnabled(on) }

// DiagnosticsLog records an app event ("ios", "android", "web") when the log is on.
func DiagnosticsLog(source, message string) { mobile.DiagnosticsLog(source, message) }

// DiagnosticsSize returns the recorded log's size in bytes.
func DiagnosticsSize() int64 { return mobile.DiagnosticsSize() }

// ClearDiagnostics deletes the recorded log.
func ClearDiagnostics() error { return mobile.ClearDiagnostics() }

// DiagnosticsReport returns the report as "txt" or "json".
func DiagnosticsReport(appInfoJSON, format string) (string, error) {
	return mobile.DiagnosticsReport(appInfoJSON, format)
}

// DiagnosticsExport writes the report into outDir and returns the file's path.
func DiagnosticsExport(appInfoJSON, format, outDir string) (string, error) {
	return mobile.DiagnosticsExport(appInfoJSON, format, outDir)
}

// Redact removes secrets (tokens, passwords, private keys) from a text.
func Redact(s string) string { return mobile.Redact(s) }

// CoreVersion describes the Go core's version and Go release.
func CoreVersion() string { return mobile.CoreVersion() }
