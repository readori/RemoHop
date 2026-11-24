package unattended_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/svc/desktop"
	"github.com/readori/RemoHop/internal/svc/input"
	"github.com/readori/RemoHop/internal/svc/screenshot"
	"github.com/readori/RemoHop/internal/wintest"
)

// TestUnattendedE2E installs the real service with --unattended, connects to
// it as a client and checks that it shows and controls the console session:
//
//	REMOHOP_UNATTENDED_E2E=1 go test -count=1 -v -run TestUnattendedE2E ./internal/unattended
//
// It needs an elevated prompt on a machine whose console session is the one
// the test runs in (a hosted CI runner's), and ffmpeg (REMOHOP_FFMPEG or PATH).
// It changes machine policy for the Ctrl+Alt+Del and UAC parts and restores
// it, and it LOCKS THE WORKSTATION at the end: run it on a throwaway machine.
// Parts the machine cannot show are reported as RESULT RUNNER LIMIT.
func TestUnattendedE2E(t *testing.T) {
	if os.Getenv("REMOHOP_UNATTENDED_E2E") != "1" {
		t.Skip("set REMOHOP_UNATTENDED_E2E=1 to install the service with unattended access (needs elevation; locks the workstation)")
	}
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Fatal("run elevated: installing a service needs administrator rights")
	}
	t.Logf("desktop: %s", wintest.Describe())
	var mine uint32
	windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &mine)
	mode := ""
	if console := windows.WTSGetActiveConsoleSessionId(); console != mine {
		// The service shows the console by default; a runner whose
		// desktop is a remote session needs the "active" mode.
		mode = "active"
		t.Logf("RESULT the console is session %d, the test runs in session %d: the service gets %s=active", console, mine, "REMOHOP_UNATTENDED_SESSION")
	} else {
		t.Logf("RESULT the test runs in the console session %d", mine)
	}
	ffmpeg := os.Getenv("REMOHOP_FFMPEG")
	if ffmpeg == "" {
		var err error
		if ffmpeg, err = exec.LookPath("ffmpeg"); err != nil {
			t.Fatal("ffmpeg is needed (REMOHOP_FFMPEG or PATH)")
		}
	}

	e := setup(t, ffmpeg, mode)

	t.Run("refusals", e.refusals)
	t.Run("screenshot", e.screenshotOfConsole)
	t.Run("desktop", e.desktopOfConsole)
	t.Run("input", e.inputToWindow)
	t.Run("ctrl_alt_del", e.secureAttention)
	t.Run("uac", e.uacPrompt)
	t.Run("lock", e.lockScreen) // last: the session stays locked
}

type env struct {
	bin, cfg, name, addr, pw, ffmpeg string
	conn                             *peer.Conn
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func freeUDPPort(t *testing.T) int {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

func (e *env) remohop(t *testing.T, hostEnv bool, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(e.bin, args...)
	cmd.Env = os.Environ()
	if hostEnv {
		cmd.Env = append(cmd.Env, "REMOHOP_CONFIG_DIR="+e.cfg)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (e *env) serviceLog() string {
	b, _ := os.ReadFile(filepath.Join(e.cfg, "service.log"))
	return string(b)
}

func setup(t *testing.T, ffmpeg, mode string) *env {
	tmp := t.TempDir()
	e := &env{bin: filepath.Join(tmp, "remohop.exe"), cfg: filepath.Join(tmp, "state"), name: "remohop-ua-e2e", pw: randHex(12), ffmpeg: ffmpeg}
	e.addr = "127.0.0.1:" + strconv.Itoa(freeUDPPort(t))
	if out, err := exec.Command("go", "build", "-o", e.bin, "github.com/readori/RemoHop/cmd/remohop").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	for _, kv := range [][2]string{{"host-password", e.pw}, {"host-listen", e.addr}} {
		if out, err := e.remohop(t, true, "config", "set", kv[0], kv[1]); err != nil {
			t.Fatalf("config set %s: %v\n%s", kv[0], err, out)
		}
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("service log (tail):\n%s", tail(e.serviceLog(), 150))
		}
		if e.conn != nil {
			e.conn.Close()
		}
		if out, err := e.remohop(t, false, "service", "uninstall", "--name", e.name); err != nil {
			t.Errorf("cleanup uninstall: %v\n%s", err, out)
		}
	})
	out, err := e.remohop(t, false, "service", "install", "--name", e.name, "--exe", e.bin, "--no-signal", "--config-dir", e.cfg, "--unattended")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	t.Logf("install:\n%s", out)
	// The service finds ffmpeg through its own environment.
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\`+e.name, registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	vars := []string{"REMOHOP_FFMPEG=" + ffmpeg}
	if mode != "" {
		vars = append(vars, "REMOHOP_UNATTENDED_SESSION="+mode)
	}
	err = k.SetStringsValue("Environment", vars)
	k.Close()
	if err != nil {
		t.Fatal(err)
	}
	if out, err := e.remohop(t, false, "service", "restart", "--name", e.name); err != nil {
		t.Fatalf("restart: %v\n%s", err, out)
	}
	out, _ = e.remohop(t, false, "service", "status", "--json", "--name", e.name)
	var st struct {
		Running    bool `json:"running"`
		Unattended bool `json:"unattended"`
	}
	json.Unmarshal([]byte(out), &st)
	if !st.Unattended {
		t.Errorf("service status does not report unattended access: %s", out)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	clientDir := t.TempDir()
	for {
		c, err := dial.Dial(ctx, e.addr, dial.Options{Dir: clientDir, Password: e.pw})
		if err == nil {
			e.conn = c
			break
		}
		if ctx.Err() != nil {
			t.Fatalf("cannot reach the service's host at %s: %v\nservice log:\n%s", e.addr, err, tail(e.serviceLog(), 80))
		}
		time.Sleep(time.Second)
	}
	if !strings.Contains(e.serviceLog(), "unattended: enabled") {
		t.Fatalf("the service did not enable unattended access:\n%s", tail(e.serviceLog(), 80))
	}
	t.Logf("RESULT service with unattended access is up at %s", e.addr)
	return e
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// refusals: the helper does nothing when not started by the service, and
// its pipe does not admit an administrator.
func (e *env) refusals(t *testing.T) {
	cmd := exec.Command(e.bin, "service", "session-helper")
	cmd.Stdin = strings.NewReader(`{"pipe":"\\\\.\\pipe\\remohop-ua-00000000000000000000000000000000","secret":"` + strings.Repeat("00", 32) + `","parent":4,"session":1}` + "\n")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "started by the RemoHop service") {
		t.Errorf("a session helper started by an administrator ran: %v\n%s", err, out)
	} else {
		t.Logf("RESULT a helper started by hand refuses: %s", strings.TrimSpace(string(out)))
	}
	// Make sure a helper runs, then try its pipe.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := screenshot.Displays(ctx, e.conn); err != nil {
		t.Fatalf("displays through the service: %v", err)
	}
	entries, err := os.ReadDir(`\\.\pipe\`)
	if err != nil {
		t.Logf("RESULT cannot list pipes here (%v); pipe access list not checked", err)
		return
	}
	found := 0
	for _, de := range entries {
		if !strings.HasPrefix(de.Name(), "remohop-ua-") {
			continue
		}
		found++
		p, _ := windows.UTF16PtrFromString(`\\.\pipe\` + de.Name())
		h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
		if err == nil {
			windows.CloseHandle(h)
			t.Errorf("an elevated administrator opened the helper pipe %s", de.Name())
		} else {
			t.Logf("RESULT the helper pipe refuses an elevated administrator: %v", err)
		}
	}
	if found == 0 {
		t.Errorf("no helper pipe found although a helper serves the screenshots")
	}
}

func decodePNG(t *testing.T, img *screenshot.Image) image.Image {
	t.Helper()
	m, err := png.Decode(bytes.NewReader(img.Data))
	if err != nil {
		t.Fatalf("decode screenshot: %v", err)
	}
	return m
}

func rgbOf(m image.Image, x, y int) [3]int {
	r, g, b, _ := m.At(x, y).RGBA()
	return [3]int{int(r >> 8), int(g >> 8), int(b >> 8)}
}

func near(got, want [3]int, tol int) bool {
	for i := range got {
		if d := got[i] - want[i]; d > tol || d < -tol {
			return false
		}
	}
	return true
}

func mean(m image.Image) [3]int {
	b := m.Bounds()
	var s [3]int64
	n := int64(0)
	for y := b.Min.Y; y < b.Max.Y; y += 8 {
		for x := b.Min.X; x < b.Max.X; x += 8 {
			c := rgbOf(m, x, y)
			s[0], s[1], s[2] = s[0]+int64(c[0]), s[1]+int64(c[1]), s[2]+int64(c[2])
			n++
		}
	}
	if n == 0 {
		return [3]int{}
	}
	return [3]int{int(s[0] / n), int(s[1] / n), int(s[2] / n)}
}

func (e *env) shot(t *testing.T) image.Image {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	img, err := screenshot.Capture(ctx, e.conn, screenshot.Options{Format: "png"})
	if err != nil {
		t.Fatalf("screenshot through the service: %v\nservice log:\n%s", err, tail(e.serviceLog(), 40))
	}
	return decodePNG(t, img)
}

var red = [3]int{255, 0, 0}

func (e *env) screenshotOfConsole(t *testing.T) {
	sw, sh := wintest.ScreenSize()
	w := wintest.Color(t, "FF0000")
	wintest.SetCursorPos(2, 2)
	m := e.shot(t)
	b := m.Bounds()
	t.Logf("RESULT screenshot through the service: %dx%d (screen %dx%d), mean colour %v", b.Dx(), b.Dy(), sw, sh, mean(m))
	if b.Dx() != sw || b.Dy() != sh {
		t.Errorf("screenshot is %dx%d, the console screen %dx%d", b.Dx(), b.Dy(), sw, sh)
	}
	cx, cy := w.Center()
	for _, p := range [][2]int{{cx, cy}, {sw / 4, sh / 4}, {sw - 20, sh - 20}} {
		if got := rgbOf(m, p[0], p[1]); !near(got, red, 30) {
			t.Errorf("pixel %d,%d of the service's screenshot is %v, want red (the console's red window)", p[0], p[1], got)
		}
	}
}

// record reads n frames of a desktop session into an MP4 and decodes the
// last one.
func (e *env) record(t *testing.T, ctx context.Context, sess *desktop.Session, n int) (int, int, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rec.mp4")
	rec, err := desktop.NewRecorder(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.SetStream(sess.Hello()); err != nil {
		t.Fatal(err)
	}
	for got := 0; got < n; {
		ev, err := sess.Next(ctx)
		if err != nil {
			t.Fatalf("desktop stream: %v\nservice log:\n%s", err, tail(e.serviceLog(), 40))
		}
		if ev.Hello != nil {
			if err := rec.SetStream(*ev.Hello); err != nil {
				t.Fatal(err)
			}
		}
		if ev.Frame != nil {
			if err := rec.WriteFrame(ev.Frame); err != nil {
				t.Fatal(err)
			}
			got++
		}
	}
	if err := rec.Close(); err != nil {
		t.Fatal(err)
	}
	out := path + ".ppm"
	if b, err := exec.CommandContext(ctx, e.ffmpeg, "-hide_banner", "-loglevel", "error", "-y", "-i", path,
		"-update", "1", "-f", "image2", "-c:v", "ppm", out).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg decode: %v\n%s", err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	r := bufio.NewReader(bytes.NewReader(data))
	var magic string
	var w, h, maxv int
	if _, err := fmt.Fscan(r, &magic, &w, &h, &maxv); err != nil || magic != "P6" {
		t.Fatalf("bad ppm: %v %q", err, magic)
	}
	r.ReadByte()
	rgb, _ := io.ReadAll(r)
	if len(rgb) < w*h*3 {
		t.Fatal("short ppm")
	}
	return w, h, rgb
}

func ppmAt(rgb []byte, w, x, y int) [3]int {
	i := (y*w + x) * 3
	return [3]int{int(rgb[i]), int(rgb[i+1]), int(rgb[i+2])}
}

func (e *env) openDesktop(t *testing.T, ctx context.Context) *desktop.Session {
	t.Helper()
	sess, err := desktop.Open(ctx, e.conn, desktop.Options{FPS: 10, Codec: "h264"})
	if err != nil {
		t.Fatalf("desktop session through the service: %v\nservice log:\n%s", err, tail(e.serviceLog(), 60))
	}
	return sess
}

func (e *env) desktopOfConsole(t *testing.T) {
	sw, sh := wintest.ScreenSize()
	w := wintest.Color(t, "FF0000")
	wintest.SetCursorPos(2, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sess := e.openDesktop(t, ctx)
	defer sess.Close()
	h := sess.Hello()
	t.Logf("RESULT desktop hello through the service: %dx%d %s encoder %s input=%v (%s) unsupported=%v warning=%q",
		h.Width, h.Height, h.Codec, h.Encoder, h.Input, h.InputError, h.Unsupported, h.Warning)
	if h.Width != sw || h.Height != sh {
		t.Errorf("stream is %dx%d, the console screen %dx%d", h.Width, h.Height, sw, sh)
	}
	if !h.Input {
		t.Errorf("no input through the service: %s", h.InputError)
	}
	fw, _, rgb := e.record(t, ctx, sess, 20)
	cx, cy := w.Center()
	if got := ppmAt(rgb, fw, cx, cy); !near(got, red, 45) {
		t.Errorf("decoded pixel %d,%d is %v, want red", cx, cy, got)
	}
	// The stream follows the screen.
	wintest.Color(t, "0000FF")
	wintest.SetCursorPos(2, 2)
	time.Sleep(500 * time.Millisecond)
	fw, _, rgb = e.record(t, ctx, sess, 30)
	if got := ppmAt(rgb, fw, cx, cy); !near(got, [3]int{0, 0, 255}, 45) {
		t.Errorf("after a blue window appeared the decoded pixel is %v, want blue", got)
	} else {
		t.Logf("RESULT desktop frames of the console session through the service follow the screen (red, then blue)")
	}
}

func (e *env) inputToWindow(t *testing.T) {
	sw, sh := wintest.ScreenSize()
	w := wintest.Edit(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cx, cy := w.Center()
	// The input service: click into the text box, type, a key.
	if _, err := input.Click(ctx, e.conn, cx, cy, input.ClickOptions{}); err != nil {
		t.Fatalf("click through the service: %v\nservice log:\n%s", err, tail(e.serviceLog(), 40))
	}
	if x, y, _ := wintest.CursorPos(); x != cx || y != cy {
		t.Errorf("after a click at %d,%d through the service the pointer is at %d,%d", cx, cy, x, y)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := input.Type(ctx, e.conn, "unattended 你好", input.TypeOptions{}); err != nil {
		t.Fatal(err)
	}
	want := "unattended 你好"
	if got := w.WaitText(want, 10*time.Second); got != want {
		t.Fatalf("the text box holds %q, want %q\ndesktop: %s", got, want, wintest.Describe())
	}
	// A desktop session's raw key events.
	sess := e.openDesktop(t, ctx)
	defer sess.Close()
	go func() {
		for {
			if _, err := sess.Next(ctx); err != nil {
				return
			}
		}
	}()
	nx, ny := float64(cx)/float64(sw-1), float64(cy)/float64(sh-1)
	for _, down := range []bool{true, false} {
		sess.SendInput(desktop.InputEvent{Kind: desktop.InputButton, Button: 0, Down: down, X: nx, Y: ny})
	}
	for _, down := range []bool{true, false} {
		sess.SendInput(desktop.InputEvent{Kind: desktop.InputKey, Code: "KeyQ", Down: down})
	}
	want += "q"
	if got := w.WaitText(want, 10*time.Second); got != want {
		t.Fatalf("after desktop key events the text box holds %q, want %q", got, want)
	}
	t.Logf("RESULT input through the service reaches a window in the console session (click, Unicode typing, desktop key events)")
}

// waitDesktop takes screenshots through the service (which makes the
// helper follow the input desktop) until the service log, after offset,
// says the input desktop is name. It returns the last screenshot.
func (e *env) waitDesktop(t *testing.T, offset int, name string, d time.Duration) (image.Image, bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for {
		// Check the helper's log first and take the picture afterwards: a
		// picture taken before the check can predate the switch.
		switched := false
		if log := e.serviceLog(); len(log) > offset && strings.Contains(log[offset:], "input desktop: "+name) {
			switched = true
			time.Sleep(time.Second) // let the new desktop finish drawing
		}
		m := e.shot(t)
		if switched {
			return m, true
		}
		if time.Now().After(deadline) {
			return m, false
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func setPolicy(t *testing.T, name string, v uint32) {
	t.Helper()
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	old, _, gerr := k.GetIntegerValue(name)
	if err := k.SetDWordValue(name, v); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`, registry.SET_VALUE)
		if err != nil {
			return
		}
		defer k.Close()
		if gerr != nil {
			k.DeleteValue(name)
		} else {
			k.SetDWordValue(name, uint32(old))
		}
	})
}

func policy(name string) (uint64, bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`, registry.QUERY_VALUE)
	if err != nil {
		return 0, false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(name)
	return v, err == nil
}

// secureAttention: Ctrl+Alt+Del through the service brings up the secure
// desktop, which the service captures, and Escape sent through the service
// (input on the secure desktop) dismisses it.
func (e *env) secureAttention(t *testing.T) {
	setPolicy(t, "SoftwareSASGeneration", 1)
	wintest.Color(t, "FF0000")
	wintest.SetCursorPos(2, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sess := e.openDesktop(t, ctx)
	reason, unsupported := sess.Hello().Unsupported[desktop.ControlCtrlAltDel]
	sess.Close()
	if unsupported {
		t.Fatalf("the service with the policy set still refuses Ctrl+Alt+Del: %s", reason)
	}
	offset := len(e.serviceLog())
	if _, err := input.Key(ctx, e.conn, []string{"ctrl+alt+delete"}, input.KeyOptions{}); err != nil {
		t.Fatalf("ctrl+alt+delete through the service: %v", err)
	}
	m, ok := e.waitDesktop(t, offset, "Winlogon", 20*time.Second)
	sw, sh := wintest.ScreenSize()
	center := rgbOf(m, sw/2, sh/2)
	t.Logf("RESULT after Ctrl+Alt+Del: secure desktop reached=%v; screenshot mean %v, centre %v; desktop: %s", ok, mean(m), center, wintest.Describe())
	if !ok {
		t.Fatalf("Ctrl+Alt+Del through the service did not bring up the secure desktop\nservice log:\n%s", tail(e.serviceLog()[offset:], 40))
	}
	if near(center, red, 30) {
		t.Errorf("the screenshot of the secure desktop shows the red window of the user's desktop")
	}
	// Escape on the secure desktop, through the service.
	offset = len(e.serviceLog())
	if _, err := input.Key(ctx, e.conn, []string{"escape"}, input.KeyOptions{}); err != nil {
		t.Fatalf("escape through the service: %v", err)
	}
	m, ok = e.waitDesktop(t, offset, "Default", 20*time.Second)
	if !ok {
		t.Fatalf("Escape sent through the service did not leave the secure desktop (input on Winlogon fails?)\nservice log:\n%s", tail(e.serviceLog()[offset:], 40))
	}
	time.Sleep(time.Second)
	m = e.shot(t)
	if got := rgbOf(m, sw/2, sh/2); !near(got, red, 30) {
		t.Errorf("back on the user's desktop the centre is %v, want the red window", got)
	}
	t.Logf("RESULT Ctrl+Alt+Del through the service: secure desktop captured, Escape injected on it, back to the user's desktop")
}

// uacPrompt: a program asking for elevation from the (unelevated) shell
// shows a consent prompt on the secure desktop; the service captures it and
// dismisses it with Escape. Hosted runners may have UAC off or the runner
// account elevated in the shell too; that is reported, not failed.
func (e *env) uacPrompt(t *testing.T) {
	lua, luaOK := policy("EnableLUA")
	cpba, _ := policy("ConsentPromptBehaviorAdmin")
	posd, _ := policy("PromptOnSecureDesktop")
	fat, _ := policy("FilterAdministratorToken")
	t.Logf("RESULT UAC settings: EnableLUA=%d (set %v) ConsentPromptBehaviorAdmin=%d PromptOnSecureDesktop=%d FilterAdministratorToken=%d", lua, luaOK, cpba, posd, fat)
	if luaOK && lua == 0 {
		t.Skip("RESULT RUNNER LIMIT: UAC is off on this machine (EnableLUA=0; turning it on needs a reboot), so no UAC prompt can appear")
	}
	setPolicy(t, "ConsentPromptBehaviorAdmin", 2) // prompt for consent on the secure desktop
	setPolicy(t, "PromptOnSecureDesktop", 1)
	wintest.Color(t, "FF0000")
	dir := t.TempDir()
	script := filepath.Join(dir, "elevate.cmd")
	os.WriteFile(script, []byte("@echo off\r\npowershell.exe -NoProfile -Command \"Start-Process cmd.exe -ArgumentList '/c','exit' -Verb RunAs\"\r\n"), 0o600)
	// explorer.exe runs the script in the user's unelevated shell context.
	offset := len(e.serviceLog())
	if err := exec.Command("explorer.exe", script).Start(); err != nil {
		t.Skipf("RESULT RUNNER LIMIT: cannot start a program from the shell: %v", err)
	}
	m, ok := e.waitDesktop(t, offset, "Winlogon", 30*time.Second)
	if !ok {
		t.Skipf("RESULT RUNNER LIMIT: no UAC prompt appeared within 30s (the runner's shell is probably elevated already, or explorer is not running); desktop: %s", wintest.Describe())
	}
	sw, sh := wintest.ScreenSize()
	t.Logf("RESULT UAC prompt on the secure desktop captured through the service: mean %v, centre %v", mean(m), rgbOf(m, sw/2, sh/2))
	if near(rgbOf(m, sw/2, sh/2), red, 30) {
		t.Errorf("the screenshot during the UAC prompt shows the user's red window")
	}
	offset = len(e.serviceLog())
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := input.Key(ctx, e.conn, []string{"escape"}, input.KeyOptions{}); err != nil {
		t.Fatalf("escape through the service: %v", err)
	}
	if _, ok := e.waitDesktop(t, offset, "Default", 20*time.Second); !ok {
		t.Errorf("Escape through the service did not dismiss the UAC prompt")
	} else {
		t.Logf("RESULT UAC prompt dismissed with Escape through the service")
	}
}

var procLockWorkStation = windows.NewLazySystemDLL("user32.dll").NewProc("LockWorkStation")

// lockScreen locks the workstation (through the service's lock control,
// else from the console session itself) and captures the lock screen
// through the service.
func (e *env) lockScreen(t *testing.T) {
	wintest.Color(t, "FF0000")
	wintest.SetCursorPos(2, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sess := e.openDesktop(t, ctx)
	defer sess.Close()
	go func() {
		// Keep the stream flowing (acks) while the test waits.
		for {
			if _, err := sess.Next(ctx); err != nil {
				return
			}
		}
	}()
	offset := len(e.serviceLog())
	how := "the service's lock control"
	if err := sess.Lock(ctx); err != nil {
		t.Logf("RESULT the service's lock control failed: %v", err)
	}
	m, ok := e.waitDesktop(t, offset, "Winlogon", 15*time.Second)
	if !ok {
		how = "LockWorkStation in the console session"
		if r, _, err := procLockWorkStation.Call(); r == 0 {
			t.Fatalf("LockWorkStation: %v", err)
		}
		m, ok = e.waitDesktop(t, offset, "Winlogon", 20*time.Second)
	}
	sw, sh := wintest.ScreenSize()
	center := rgbOf(m, sw/2, sh/2)
	t.Logf("RESULT locked with %s: secure desktop reached=%v; lock screen through the service: mean %v, centre %v; desktop: %s",
		how, ok, mean(m), center, wintest.Describe())
	if near(center, red, 30) {
		t.Errorf("the service's screenshot of the locked workstation shows the red window")
	}
	// Desktop frames of the lock screen.
	sess.Close()
	sess2 := e.openDesktop(t, ctx)
	defer sess2.Close()
	fw, _, rgb := e.record(t, ctx, sess2, 15)
	got := ppmAt(rgb, fw, sw/2, sh/2)
	t.Logf("RESULT desktop frame of the lock screen through the service: centre %v", got)
	if near(got, red, 45) {
		t.Errorf("the desktop stream of the locked workstation shows the red window")
	}
	if !ok {
		t.Errorf("the helper never reported the secure desktop after locking")
	}
}
