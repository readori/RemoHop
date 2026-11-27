package shell

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

// userShell picks the host's shell: %REMOHOP_SHELL% if it names an executable,
// else PowerShell 7 (pwsh.exe), else Windows PowerShell (powershell.exe),
// else cmd.exe (%ComSpec%).
func userShell() string {
	if sh := os.Getenv("REMOHOP_SHELL"); sh != "" {
		if p, err := exec.LookPath(sh); err == nil {
			return p
		}
	}
	for _, name := range []string{"pwsh.exe", "powershell.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	if c := os.Getenv("ComSpec"); c != "" {
		return c
	}
	return `C:\Windows\System32\cmd.exe`
}

type shellKind int

const (
	shellPowerShell shellKind = iota
	shellCmd
	shellPOSIX // anything else (bash.exe, ...) gets "-c"
)

func kindOf(sh string) shellKind {
	switch strings.TrimSuffix(strings.ToLower(filepath.Base(sh)), ".exe") {
	case "pwsh", "powershell":
		return shellPowerShell
	case "cmd":
		return shellCmd
	}
	return shellPOSIX
}

// psPrelude runs before a PowerShell command. Output is UTF-8 whatever the
// console code page (the setters fail harmlessly without a console),
// progress bars and colour escapes stay off the output (PowerShell 7 would
// colour error text even into a pipe; $PSStyle does not exist in 5.1), and
// $LASTEXITCODE starts clean so the epilogue can tell whether the last
// statement was a failing native command.
//
// The command line also says -InputFormat Text -OutputFormat Text: with
// -EncodedCommand PowerShell otherwise assumes another PowerShell is
// calling and switches both to CLIXML, so errors would arrive on stderr as
// "#< CLIXML <Objs ...>" and piped stdin would be parsed as XML.
const psPrelude = `$ProgressPreference = 'SilentlyContinue'
try { $PSStyle.OutputRendering = 'PlainText' } catch {}
$__remohopUTF8 = New-Object System.Text.UTF8Encoding $false
try { [Console]::OutputEncoding = $__remohopUTF8 } catch {}
try { [Console]::InputEncoding = $__remohopUTF8 } catch {}
$OutputEncoding = $__remohopUTF8
$global:LASTEXITCODE = 0
`

// psEpilogue turns the outcome of the last statement into the exit code:
// 0 if it succeeded, else the exit code of a failing native command, else 1.
// (`exit N` never gets here; an uncaught terminating error exits with 1.)
const psEpilogue = `
if ($?) { exit 0 } elseif ($LASTEXITCODE) { exit $LASTEXITCODE } else { exit 1 }
`

// maxCmdLine is CreateProcess's command line limit, in UTF-16 units.
const maxCmdLine = 32767

// commandCmd runs command through the host shell. PowerShell gets it as
// -EncodedCommand (UTF-16LE, base64), which sidesteps every quoting rule;
// cmd.exe gets it verbatim after /S /C. The command line is always set
// explicitly because Go's argument quoting follows the C runtime rules,
// which neither shell uses.
func commandCmd(command string) *exec.Cmd {
	sh := userShell()
	cmd := exec.Command(sh)
	var line string
	switch kindOf(sh) {
	case shellPowerShell:
		line = windows.EscapeArg(sh) + " -NoLogo -NoProfile -NonInteractive -InputFormat Text -OutputFormat Text -EncodedCommand " +
			encodePS(psPrelude+command+psEpilogue)
	case shellCmd:
		line = windows.EscapeArg(sh) + ` /S /C "` + command + `"`
	default:
		line = windows.ComposeCommandLine([]string{sh, "-c", command})
	}
	if len(utf16.Encode([]rune(line))) >= maxCmdLine && cmd.Err == nil {
		cmd.Err = errors.New("command too long for a Windows command line")
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line}
	return cmd
}

func encodePS(script string) string {
	u := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(u))
	for i, c := range u {
		binary.LittleEndian.PutUint16(b[2*i:], c)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// loginShellCmd is the interactive shell of a session.
func loginShellCmd() *exec.Cmd {
	sh := userShell()
	cmd := exec.Command(sh)
	line := windows.EscapeArg(sh)
	if kindOf(sh) == shellPowerShell {
		line += " -NoLogo"
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: line}
	return cmd
}

// setDetached gives a background job its own process group and its own
// hidden console: it survives the host, and console APIs (such as setting
// the output code page) work without touching the host's console.
func setDetached(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= syscall.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW
}

// killGroup kills pid and its whole process tree. Windows has no process
// groups in the POSIX sense; taskkill /T walks the parent links instead.
func killGroup(pid int) {
	if pid > 0 {
		exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
	}
}

// signalGroup supports only termination on Windows.
func signalGroup(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return errors.New("invalid pid")
	}
	switch sig {
	case syscall.SIGKILL, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP:
		return exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
	}
	return fmt.Errorf("signal %s is not supported on windows", signalName(sig))
}

func exitInfo(ps *os.ProcessState) (code, sig int, name string) {
	return ps.ExitCode(), 0, ""
}

var signalNames = map[syscall.Signal]string{
	syscall.SIGHUP: "HUP", syscall.SIGINT: "INT", syscall.SIGKILL: "KILL", syscall.SIGTERM: "TERM",
}

func signalName(s syscall.Signal) string {
	if n, ok := signalNames[s]; ok {
		return n
	}
	return strconv.Itoa(int(s))
}

func parseSignal(s string) (syscall.Signal, error) {
	if n, err := strconv.Atoi(s); err == nil {
		return syscall.Signal(n), nil
	}
	name := strings.TrimPrefix(strings.ToUpper(s), "SIG")
	for sig, n := range signalNames {
		if n == name {
			return sig, nil
		}
	}
	return 0, fmt.Errorf("unknown signal %q", s)
}

// procStart returns the process creation time, which identifies it across
// PID reuse.
func procStart(pid int) uint64 {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(h)
	var c, e, k, u windows.Filetime
	if windows.GetProcessTimes(h, &c, &e, &k, &u) != nil {
		return 0
	}
	return uint64(c.HighDateTime)<<32 | uint64(c.LowDateTime)
}

func procAlive(pid int, start uint64) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	var code uint32
	if windows.GetExitCodeProcess(h, &code) != nil || code != 259 { // STILL_ACTIVE
		return false
	}
	return start == 0 || procStart(pid) == start
}

// exitShowsSignal: a killed Windows process merely exits with a code.
const exitShowsSignal = false

// pipedEnv is extra environment for commands that run without a terminal.
// PowerShell 7 colours error text with escape sequences even when stderr is
// a pipe; the prelude turns that off, but a script that fails to parse
// never runs its prelude, so plain rendering has to come from outside the
// script. NO_COLOR is the switch PowerShell (7.2+) honours for that.
func pipedEnv() []string {
	if kindOf(userShell()) == shellPowerShell {
		return []string{"NO_COLOR=1"}
	}
	return nil
}

// shellEnv is environment the host sets for every process it starts
// through the shell. cmd.exe looks for a program in the current directory
// before PATH, and Windows' own program search (CreateProcess, SearchPath)
// does the same for a bare name: a program planted in the working
// directory under an allowed name would run instead of the real one.
// NoDefaultCurrentDirectoryInExePath turns that off for cmd.exe, and for
// PowerShell and what it starts. A client may still name a program in the
// current directory explicitly (.\prog), which a command allow-list does
// not match.
func shellEnv() []string {
	switch kindOf(userShell()) {
	case shellCmd, shellPowerShell:
		return []string{"NoDefaultCurrentDirectoryInExePath=1"}
	}
	return nil
}
