package desktop

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/readori/RemoHop/internal/config"
)

// macOS: a LaunchAgent that runs the app at login (it shows in System
// Settings → General → Login Items). Its label differs from the host
// service's (com.remohop.host).
const agentLabel = "com.remohop.app"

func agentPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Library", "LaunchAgents", agentLabel+".plist"), nil
}

func xmlText(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

func agentPlist(exe string) string {
	var args strings.Builder
	for _, a := range append([]string{exe}, autostartArgs...) {
		args.WriteString("\t\t<string>" + xmlText(a) + "</string>\n")
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>` + agentLabel + `</string>
	<key>ProgramArguments</key>
	<array>
` + args.String() + `	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>ProcessType</key>
	<string>Interactive</string>
</dict>
</plist>
`
}

func autostartOn(exe string) (bool, bool) {
	p, err := agentPath()
	if err != nil {
		return false, false
	}
	b, err := os.ReadFile(p)
	return err == nil && string(b) == agentPlist(exe), true
}

func writeAutostart(exe string, on bool) error {
	p, err := agentPath()
	if err != nil {
		return err
	}
	if !on {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return config.WriteFileAtomic(p, []byte(agentPlist(exe)), 0o644)
}
