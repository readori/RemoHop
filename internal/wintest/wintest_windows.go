package wintest

import (
	"bytes"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procSetCursorPos        = user32.NewProc("SetCursorPos")
	procGetSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procSetProcessDPIAware  = user32.NewProc("SetProcessDPIAware")
	procGetProcessWinStn    = user32.NewProc("GetProcessWindowStation")
	procGetThreadDesktop    = user32.NewProc("GetThreadDesktop")
	procGetUserObjectInfoW  = user32.NewProc("GetUserObjectInformationW")
	procOpenInputDesktop    = user32.NewProc("OpenInputDesktop")
	procCloseDesktop        = user32.NewProc("CloseDesktop")
	procGetForegroundWindow = user32.NewProc("GetForegroundWindow")
	procGetWindowTextW      = user32.NewProc("GetWindowTextW")
	procKeybdEvent          = user32.NewProc("keybd_event")
	procWindowFromPoint     = user32.NewProc("WindowFromPoint")
	procGetWindowThreadPID  = user32.NewProc("GetWindowThreadProcessId")
	dpiOnce                 sync.Once
)

// DPIAware makes this process report physical pixels (as the host does).
func DPIAware() { dpiOnce.Do(func() { procSetProcessDPIAware.Call() }) }

// CursorPos is the pointer position (GetCursorPos).
func CursorPos() (x, y int, err error) {
	DPIAware()
	var p struct{ x, y int32 }
	if r, _, e := procGetCursorPos.Call(uintptr(unsafe.Pointer(&p))); r == 0 {
		return 0, 0, fmt.Errorf("GetCursorPos: %v", e)
	}
	return int(p.x), int(p.y), nil
}

// SetCursorPos moves the pointer directly, without SendInput (to park it
// somewhere harmless).
func SetCursorPos(x, y int) error {
	DPIAware()
	if r, _, e := procSetCursorPos.Call(uintptr(x), uintptr(y)); r == 0 {
		return fmt.Errorf("SetCursorPos: %v", e)
	}
	return nil
}

// ScreenSize is the primary monitor's size in physical pixels.
func ScreenSize() (w, h int) {
	DPIAware()
	a, _, _ := procGetSystemMetrics.Call(0)
	b, _, _ := procGetSystemMetrics.Call(1)
	return int(a), int(b)
}

func objectName(h uintptr) string {
	var buf [256]uint16
	var n uint32
	if r, _, _ := procGetUserObjectInfoW.Call(h, 2 /* UOI_NAME */, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2), uintptr(unsafe.Pointer(&n))); r == 0 {
		return "?"
	}
	return windows.UTF16ToString(buf[:])
}

// Describe says what desktop this process is on: the user, the session, the
// window station and desktop, whether the input desktop can be opened (it
// cannot when the session is locked or at the secure desktop), the screen
// size and the foreground window.
func Describe() string {
	var b strings.Builder
	if u, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil {
		if acct, dom, _, err := u.User.Sid.LookupAccount(""); err == nil {
			fmt.Fprintf(&b, "user %s\\%s; ", dom, acct)
		}
	}
	var sid uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &sid); err == nil {
		fmt.Fprintf(&b, "session %d (console session %d); ", sid, windows.WTSGetActiveConsoleSessionId())
	}
	ws, _, _ := procGetProcessWinStn.Call()
	tid := windows.GetCurrentThreadId()
	dk, _, _ := procGetThreadDesktop.Call(uintptr(tid))
	fmt.Fprintf(&b, "window station %q desktop %q; ", objectName(ws), objectName(dk))
	if in, _, e := procOpenInputDesktop.Call(0, 0, 0x0100 /* DESKTOP_SWITCHDESKTOP */); in != 0 {
		fmt.Fprintf(&b, "input desktop %q; ", objectName(in))
		procCloseDesktop.Call(in)
	} else {
		fmt.Fprintf(&b, "input desktop not accessible (%v); ", e)
	}
	w, h := ScreenSize()
	fmt.Fprintf(&b, "primary screen %dx%d; ", w, h)
	if x, y, err := CursorPos(); err == nil {
		fmt.Fprintf(&b, "cursor %d,%d; ", x, y)
	}
	if fg, _, _ := procGetForegroundWindow.Call(); fg != 0 {
		_, d := windowDesc(fg)
		fmt.Fprintf(&b, "foreground window %s", d)
	} else {
		b.WriteString("no foreground window")
	}
	return b.String()
}

// windowAt describes the top-level-most window at a screen point: its title and
// the process that owns it.
func windowAt(x, y int) (pid uint32, desc string) {
	hwnd, _, _ := procWindowFromPoint.Call(uintptr(uint64(uint32(int32(x))) | uint64(uint32(int32(y)))<<32))
	if hwnd == 0 {
		return 0, "no window"
	}
	return windowDesc(hwnd)
}

func windowDesc(hwnd uintptr) (pid uint32, desc string) {
	procGetWindowThreadPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	var t [256]uint16
	procGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&t[0])), uintptr(len(t)))
	return pid, fmt.Sprintf("%q of process %s (pid %d)", windows.UTF16ToString(t[:]), imageName(pid), pid)
}

func imageName(pid uint32) string {
	name := "?"
	if h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid); err == nil {
		var b [520]uint16
		n := uint32(len(b))
		if windows.QueryFullProcessImageName(h, 0, &b[0], &n) == nil {
			name = filepath.Base(windows.UTF16ToString(b[:n]))
		}
		windows.CloseHandle(h)
	}
	return name
}

// dismiss gets rid of a window that a runner image puts above every other
// one: the "Microsoft account" sign-in dialog (its host process,
// WWAHost.exe, is ended) and the Start menu or Search flyout (closed with
// Escape). It reports whether it did something.
func dismiss(pid uint32) bool {
	switch name := strings.ToLower(imageName(pid)); name {
	case "wwahost.exe":
		h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
		if err != nil {
			return false
		}
		defer windows.CloseHandle(h)
		return windows.TerminateProcess(h, 1) == nil
	case "startmenuexperiencehost.exe", "searchhost.exe", "searchapp.exe", "shellexperiencehost.exe":
		const vkEscape, keyUp = 0x1b, 2
		procKeybdEvent.Call(vkEscape, 0, 0, 0)
		procKeybdEvent.Call(vkEscape, 0, keyUp, 0)
		return true
	}
	return false
}

// Window is a helper window in a PowerShell process.
type Window struct {
	// Bounds is where the content (the whole window for Color, the text
	// box for Edit) is on the screen, in physical pixels.
	Bounds image.Rectangle

	textFile string
	cmd      *exec.Cmd
	out      *lockedBuf
}

type lockedBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuf) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

const scriptHead = `param([string]$Out, [string]$Ready, [string]$Arg)
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
try { Add-Type -Namespace W -Name U -MemberDefinition '[DllImport("user32.dll")] public static extern bool SetProcessDPIAware();'; [void][W.U]::SetProcessDPIAware() } catch {}
$script:Out = $Out; $script:Ready = $Ready
$script:enc = New-Object System.Text.UTF8Encoding($false)
$f = New-Object System.Windows.Forms.Form
$f.FormBorderStyle = 'None'
$f.StartPosition = 'Manual'
$f.TopMost = $true
`

// colorScript fills the primary monitor with the colour in $Arg (RRGGBB).
const colorScript = scriptHead + `
$c = [System.Drawing.ColorTranslator]::FromHtml('#' + $Arg)
$f.BackColor = $c
$b = [System.Windows.Forms.SystemInformation]::PrimaryMonitorSize
$f.Bounds = New-Object System.Drawing.Rectangle(0, 0, $b.Width, $b.Height)
$f.Add_Shown({
  $f.Activate()
  [IO.File]::WriteAllText($script:Ready, ("0 0 {0} {1}" -f $f.Width, $f.Height))
})
[System.Windows.Forms.Application]::Run($f)
`

// editScript shows a 600x300 multi-line text box at 100,100 that writes its
// text to $Out on every change.
const editScript = scriptHead + `
$f.Bounds = New-Object System.Drawing.Rectangle(100, 100, 600, 300)
$tb = New-Object System.Windows.Forms.TextBox
$tb.Multiline = $true
$tb.AcceptsReturn = $true
$tb.AcceptsTab = $true
$tb.Dock = 'Fill'
$tb.Font = New-Object System.Drawing.Font('Consolas', 14)
$f.Controls.Add($tb)
# .NET Framework's TextBox has no Ctrl+A.
$tb.Add_KeyDown({ param($s, $e) if ($e.Control -and $e.KeyCode -eq 'A') { $tb.SelectAll(); $e.SuppressKeyPress = $true } })
[IO.File]::WriteAllText($script:Out, '', $script:enc)
$tb.Add_TextChanged({ [IO.File]::WriteAllText($script:Out, $tb.Text, $script:enc) })
$f.Add_Shown({
  $f.Activate()
  $tb.Focus() | Out-Null
  $p = $tb.PointToScreen([System.Drawing.Point]::Empty)
  [IO.File]::WriteAllText($script:Ready, ("{0} {1} {2} {3}" -f $p.X, $p.Y, $tb.Width, $tb.Height))
})
[System.Windows.Forms.Application]::Run($f)
`

func start(t testing.TB, script, arg string) *Window {
	t.Helper()
	dir := t.TempDir()
	sp := filepath.Join(dir, "window.ps1")
	if err := os.WriteFile(sp, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	w := &Window{textFile: filepath.Join(dir, "text.txt"), out: &lockedBuf{}}
	ready := filepath.Join(dir, "ready.txt")
	w.cmd = exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-STA", "-ExecutionPolicy", "Bypass",
		"-File", sp, "-Out", w.textFile, "-Ready", ready, "-Arg", arg)
	w.cmd.Stdout, w.cmd.Stderr = w.out, w.out
	if err := w.cmd.Start(); err != nil {
		t.Fatalf("starting powershell: %v", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- w.cmd.Wait() }()
	t.Cleanup(func() {
		w.cmd.Process.Kill()
		<-exited
	})
	deadline := time.Now().Add(60 * time.Second)
	for {
		if b, err := os.ReadFile(ready); err == nil {
			f := strings.Fields(string(b))
			if len(f) == 4 {
				var v [4]int
				for i := range v {
					v[i], _ = strconv.Atoi(f[i])
				}
				w.Bounds = image.Rect(v[0], v[1], v[0]+v[2], v[1]+v[3])
				break
			}
		}
		select {
		case err := <-exited:
			t.Fatalf("the window process exited (%v) before showing a window:\n%s\n%s", err, w.out.String(), Describe())
		case <-time.After(100 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatalf("no window within 60s:\n%s\n%s", w.out.String(), Describe())
		}
	}
	time.Sleep(700 * time.Millisecond) // let it paint and settle
	// Something else on the desktop (a first-run dialog, a notification) can
	// sit above the test window; then clicks and screenshots would hit that.
	cx, cy := w.Center()
	for attempt := 0; ; attempt++ {
		pid, desc := windowAt(cx, cy)
		if pid == uint32(w.cmd.Process.Pid) {
			break
		}
		if attempt < 8 && dismiss(pid) {
			t.Logf("dismissed a window covering the test window: %s", desc)
			time.Sleep(1500 * time.Millisecond)
			continue
		}
		// Not a product failure: the runner's own shell is in the way.
		t.Skipf("RESULT RUNNER LIMIT: another window covers the test window at %d,%d and cannot be dismissed: %s\n%s", cx, cy, desc, Describe())
	}
	return w
}

// Color shows a borderless always-on-top window of one colour ("RRGGBB")
// over the whole primary monitor.
func Color(t testing.TB, rrggbb string) *Window { return start(t, colorScript, rrggbb) }

// Edit shows an always-on-top window that is one multi-line text box.
func Edit(t testing.TB) *Window { return start(t, editScript, "") }

// Center is the middle of the window's content.
func (w *Window) Center() (x, y int) {
	return (w.Bounds.Min.X + w.Bounds.Max.X) / 2, (w.Bounds.Min.Y + w.Bounds.Max.Y) / 2
}

// Text is what the Edit window's text box holds, with CRLF as "\n".
func (w *Window) Text() string {
	b, _ := os.ReadFile(w.textFile)
	return strings.ReplaceAll(string(b), "\r\n", "\n")
}

// WaitText polls until the text box holds want (or time runs out) and
// returns the last text seen.
func (w *Window) WaitText(want string, d time.Duration) string {
	deadline := time.Now().Add(d)
	for {
		got := w.Text()
		if got == want || time.Now().After(deadline) {
			return got
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Log is what the helper process printed (errors of the script).
func (w *Window) Log() string { return w.out.String() }
