package grant

import (
	"strings"
	"testing"
)

// cmd.exe does not treat single quotes as quotes, and expands % and ! even
// inside double quotes: the allow-list must judge a command by the rules of
// the shell that will run it (security audit finding 9).
func TestCheckCommandDialects(t *testing.T) {
	allow := []string{"git", "dir"}
	cases := []struct {
		cmd              string
		posix, ps, cmdOK bool
	}{
		{"git status", true, true, true},
		{`git commit -m "a & b"`, true, true, true},
		{"git 'x & whoami & rem '", true, true, false},
		{"git 'x | whoami'", true, true, false},
		{"git log --format=%H", true, true, false},
		{`git "%COMSPEC%"`, true, true, false},
		{"git a^&whoami", false, false, false},
		{"git x^ & whoami", false, false, false},
		{"git !x!", true, true, false},
		{"git it's", false, false, true}, // an unbalanced quote to sh and PowerShell, a letter to cmd
		{"dir C:\\Users", true, true, true},
	}
	for _, c := range cases {
		for _, d := range []struct {
			dialect Dialect
			want    bool
		}{{DialectPOSIX, c.posix}, {DialectPowerShell, c.ps}, {DialectCmd, c.cmdOK}} {
			err := checkCommand(c.cmd, allow, d.dialect, "sh")
			if (err == nil) != d.want {
				t.Errorf("dialect %d, %q: %v (want allowed=%v)", d.dialect, c.cmd, err, d.want)
			}
		}
	}
	if err := checkCommand("git status", allow, DialectUnknown, "/usr/bin/nu"); err == nil || !strings.Contains(err.Error(), "/usr/bin/nu") {
		t.Errorf("unknown shell: %v", err)
	}
	// Without an allow-list nothing is checked, whatever the shell.
	old := HostShell
	defer func() { HostShell = old }()
	HostShell = func() (Dialect, string) { return DialectUnknown, "/usr/bin/nu" }
	if err := CheckCommand("anything | goes", nil); err != nil {
		t.Errorf("no allow-list: %v", err)
	}
	HostShell = func() (Dialect, string) { return DialectCmd, `C:\Windows\System32\cmd.exe` }
	if err := CheckCommand("git 'x & whoami & rem '", allow); err == nil {
		t.Error("the cmd.exe bypass passes CheckCommand")
	}
}
