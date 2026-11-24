package grant

import (
	"bufio"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/perm"
)

func mustParse(t *testing.T, allow []string, cmds ...string) *Rules {
	t.Helper()
	r, err := Parse(allow, cmds)
	if err != nil {
		t.Fatalf("Parse(%q, %q): %v", allow, cmds, err)
	}
	return r
}

func init() {
	// Services the tests name that live in packages this one does not
	// import.
	for s, f := range map[string]perm.Feature{"vpn": perm.VPN, "usb": perm.USB, "serial": perm.Serial, "forward.udp": perm.Forward} {
		if _, ok := perm.Of(s); !ok {
			perm.Register(s, f)
		}
	}
}

func TestParseServices(t *testing.T) {
	cases := []struct {
		allow   string
		yes, no []string
		shell   ShellMode
		input   bool
	}{
		{"shell", []string{"shell", "exec", "spawn", "jobs"}, []string{"fs", "screenshot", "forward"}, ShellFull, false},
		{"shell:run-only", []string{"exec", "spawn", "jobs"}, []string{"shell", "fs"}, ShellRunOnly, false},
		{"shell:cmd=git", []string{"exec", "spawn", "jobs"}, []string{"shell"}, ShellRunOnly, false},
		{"files", []string{"fs", "fs.get", "fs.put"}, []string{"exec", "shell"}, ShellNone, false},
		{"files:read", []string{"fs", "fs.get"}, []string{"fs.put", "exec"}, ShellNone, false},
		{"files:read:/srv", []string{"fs", "fs.get"}, []string{"fs.put"}, ShellNone, false},
		{"files:write:/srv", []string{"fs", "fs.get", "fs.put"}, []string{"exec"}, ShellNone, false},
		{"screenshot", []string{"screenshot"}, []string{"desktop", "input", "clip"}, ShellNone, false},
		{"screen", []string{"screenshot", "desktop"}, []string{"input"}, ShellNone, false},
		{"desktop", []string{"screenshot", "desktop", "input"}, []string{"clip"}, ShellNone, true},
		{"input", []string{"input"}, []string{"desktop"}, ShellNone, true},
		{"clip", []string{"clip"}, []string{"screenshot"}, ShellNone, false},
		{"info", []string{"sysinfo"}, []string{"exec"}, ShellNone, false},
		{"forward", []string{"forward", "forward.listen", "forward.udp"}, []string{"vpn"}, ShellNone, false},
		{"vpn", []string{"vpn"}, []string{"forward", "usb"}, ShellNone, false},
		{"usb", []string{"usb"}, []string{"vpn"}, ShellNone, false},
		{"serial", []string{"serial"}, []string{"usb"}, ShellNone, false},
		{"", nil, []string{"exec", "fs", "screenshot", "forward", "vpn", "usb", "clip", "input", "desktop", "pair-unknown"}, ShellNone, false},
	}
	for _, c := range cases {
		allow := []string{}
		if c.allow != "" {
			allow = append(allow, strings.ReplaceAll(c.allow, "/srv", filepath.FromSlash(absRoot())))
		}
		r := mustParse(t, allow)
		for _, s := range c.yes {
			if !r.AllowsService(s) {
				t.Errorf("%q: %s should be allowed", c.allow, s)
			}
		}
		for _, s := range c.no {
			if r.AllowsService(s) {
				t.Errorf("%q: %s should be refused", c.allow, s)
			}
		}
		if r.Shell != c.shell || r.Input != c.input || r.AllowsFeature(perm.Input) != c.input {
			t.Errorf("%q: shell %v input %v", c.allow, r.Shell, r.Input)
		}
	}
}

// absRoot is an absolute path on any OS.
func absRoot() string {
	if runtime.GOOS == "windows" {
		return `C:\srv`
	}
	return "/srv"
}

func TestParseErrors(t *testing.T) {
	for _, allow := range []string{"bogus", "shell:sometimes", "shell:cmd=", "files:maybe", "files:read:relative/path", "screenshot:x", "clip:read"} {
		if _, err := Parse([]string{allow}, nil); err == nil {
			t.Errorf("Parse(%q) succeeded", allow)
		}
	}
	if _, err := Parse([]string{"shell"}, []string{"git"}); err == nil {
		t.Error("a command list with full shell must be refused")
	}
}

func TestParseCombined(t *testing.T) {
	root := absRoot()
	r := mustParse(t, []string{"shell:run-only, files:read:" + root + ",screenshot", "files:write:" + filepath.Join(root, "out")}, "go test", "  git   status ")
	if r.Shell != ShellRunOnly || len(r.Commands) != 2 || r.Commands[1] != "git status" {
		t.Fatalf("rules: %+v", r)
	}
	if len(r.ReadRoots) != 1 || len(r.WriteRoots) != 1 || r.ReadAll || r.WriteAll {
		t.Fatalf("roots: %+v", r)
	}
	if got := strings.Join(r.Summary(), "|"); !strings.Contains(got, "go test; git status") || !strings.Contains(got, "screenshot") {
		t.Errorf("summary: %s", got)
	}
	denied := strings.Join(r.Denied(), ",")
	for _, want := range []string{"interactive shell", "input", "clip", "forward", "vpn", "usb", "desktop viewing"} {
		if !strings.Contains(denied, want) {
			t.Errorf("denied %q lacks %q", denied, want)
		}
	}
	// files:read anywhere drops read roots; files (write anywhere) drops all.
	if r := mustParse(t, []string{"files:read", "files:read:" + root}); !r.ReadAll || r.ReadRoots != nil {
		t.Errorf("files:read + root: %+v", r)
	}
	if r := mustParse(t, []string{"files", "files:write:" + root}); !r.WriteAll || r.WriteRoots != nil {
		t.Errorf("files + root: %+v", r)
	}
	if home, err := os.UserHomeDir(); err == nil {
		r := mustParse(t, []string{"files:read:~/project"})
		if r.ReadRoots[0] != filepath.Join(home, "project") {
			t.Errorf("~ not expanded: %v", r.ReadRoots)
		}
	}
}

func TestCheckCommand(t *testing.T) {
	allow := []string{"git", "go test", "ls"}
	ok := []string{"git status", "git", "git log -1 --format='%H %s'", `go test ./... -run "TestX"`, "ls -la /tmp"}
	bad := []string{"rm -rf /", "go run .", "gitx status", "go", "git status; rm -rf /", "git status && reboot", "ls | sh",
		"ls > /etc/passwd", "git $(reboot)", "git `reboot`", "git status\nreboot", "ls 'unbalanced", "git log & sleep",
		"FOO=1 git status", "git (x)", "ls {a,b}", `git "$HOME"`,
		`git \"; whoami; \"`, `git "a\"" ; whoami ; "`, "git \"a\u201d ; whoami; \u201cb\"", "git status\u2028whoami", "git\x0bstatus"}
	for _, c := range ok {
		if err := CheckCommand(c, allow); err != nil {
			t.Errorf("%q refused: %v", c, err)
		}
	}
	for _, c := range bad {
		if err := CheckCommand(c, allow); err == nil {
			t.Errorf("%q allowed", c)
		}
	}
	if err := CheckCommand("anything at all; really", nil); err != nil {
		t.Errorf("no allow-list must allow everything: %v", err)
	}
	// Quoted operators are plain text.
	if err := CheckCommand(`git commit -m "fix; tidy | up"`, allow); err != nil {
		t.Errorf("quoted operators: %v", err)
	}
}

func TestCheckPath(t *testing.T) {
	base := t.TempDir()
	proj := filepath.Join(base, "project")
	out := filepath.Join(proj, "out")
	secret := filepath.Join(base, "secret")
	for _, d := range []string{out, secret} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(proj, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(secret, "key"), []byte("k"), 0o600)
	r := mustParse(t, []string{"files:read:" + proj, "files:write:" + out})

	allow := []struct {
		p string
		a Access
	}{
		{proj, Read}, {filepath.Join(proj, "a.txt"), Read}, {filepath.Join(proj, "new", "deeper"), Read},
		{filepath.Join(out, "x.bin"), Write}, {out, Read}, {filepath.Join(out, "sub", "y"), Write},
		{base, Meta}, // an ancestor of a root may be looked at
	}
	for _, c := range allow {
		if err := r.CheckPath(c.p, c.a); err != nil {
			t.Errorf("%s %v refused: %v", c.p, c.a, err)
		}
	}
	deny := []struct {
		p string
		a Access
	}{
		{filepath.Join(secret, "key"), Read}, {filepath.Join(proj, "a.txt"), Write}, {proj, Write},
		{base, Read}, // ... but not listed
		{filepath.Join(out, "..", "a.txt"), Write}, {filepath.Join(proj, "..", "secret", "key"), Read},
		{"relative/path", Read},
	}
	for _, c := range deny {
		err := r.CheckPath(c.p, c.a)
		if err == nil {
			t.Errorf("%s %v allowed", c.p, c.a)
		}
	}
	if err := r.CheckPath(filepath.Join(secret, "key"), Read); err == nil || !strings.Contains(err.Error(), "outside the readable roots") {
		t.Errorf("refusal text: %v", err)
	}

	if runtime.GOOS != "windows" {
		// A link inside a root leads nowhere outside it, in either
		// direction of resolution.
		if err := os.Symlink(secret, filepath.Join(proj, "escape")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(secret, "key"), filepath.Join(out, "keylink")); err != nil {
			t.Fatal(err)
		}
		for _, p := range []string{filepath.Join(proj, "escape", "key"), filepath.Join(proj, "escape")} {
			if err := r.CheckPath(p, Read); err == nil {
				t.Errorf("read through link %s allowed", p)
			}
		}
		if err := r.CheckPath(filepath.Join(out, "keylink"), Write); err == nil {
			t.Error("write through link allowed")
		}
		// A root that is itself reached through a link works.
		alias := filepath.Join(base, "alias")
		os.Symlink(proj, alias)
		ra := mustParse(t, []string{"files:read:" + alias})
		if err := ra.CheckPath(filepath.Join(proj, "a.txt"), Read); err != nil {
			t.Errorf("root through a link: %v", err)
		}
	}

	none := mustParse(t, []string{"screenshot"})
	if err := none.CheckPath(proj, Read); err == nil {
		t.Error("no file items must allow no file access")
	}
	all := mustParse(t, []string{"files:read"})
	if all.CheckPath(filepath.Join(secret, "key"), Read) != nil || all.CheckPath(filepath.Join(secret, "key"), Write) == nil {
		t.Error("files:read reads anywhere and writes nowhere")
	}
}

func TestRefusal(t *testing.T) {
	err := error(&Refusal{Grant: "claude-session", What: "files:write"})
	if err.Error() != "files:write is not allowed by grant claude-session" {
		t.Errorf("text: %q", err)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Error("a refusal should match fs.ErrPermission")
	}
	if s := (&Refusal{Grant: "g", What: "x", Why: "y"}).Error(); s != "x is not allowed by grant g (y)" {
		t.Errorf("text with reason: %q", s)
	}
}

func TestCode(t *testing.T) {
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(i * 7)
	}
	code := EncodeCode("0123456789abcdef", secret)
	id, sec, err := ParseCode("  " + code + "\n")
	if err != nil || id != "0123456789abcdef" || string(sec) != string(secret) {
		t.Fatalf("round trip: %q %x %v", id, sec, err)
	}
	for _, bad := range []string{"", "remohopg1-", "remohopg1-zz-AAAA", "nope-0123-AAAA", code[:len(code)-2], "remohopg1-0123456789abcdef"} {
		if _, _, err := ParseCode(bad); err == nil {
			t.Errorf("ParseCode(%q) succeeded", bad)
		}
	}
}

func TestStateAndNames(t *testing.T) {
	now := time.Now()
	g := &Grant{Name: "agent", Expires: now.Add(time.Minute)}
	if g.StateAt(now) != StateActive || g.Inactive(now) != nil {
		t.Error("should be active")
	}
	if g.StateAt(now.Add(time.Minute)) != StateExpired || !strings.Contains(g.Inactive(now.Add(2*time.Minute)).Error(), "expired") {
		t.Error("should be expired")
	}
	g.Suspended = true
	if g.StateAt(now) != StateSuspended || !strings.Contains(g.Inactive(now).Error(), "suspended") {
		t.Error("should be suspended")
	}
	for _, ok := range []string{"claude-session", "a", "ci.bot_2"} {
		if ValidName(ok) != nil {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "-x", "has space", "a/b", strings.Repeat("x", 65), "ünï"} {
		if ValidName(bad) == nil {
			t.Errorf("%q should be invalid", bad)
		}
	}
	for in, want := range map[string]string{"": "none", "each": "each", "FIRST": "first", "none": "none"} {
		if got, err := ParseApprove(in); err != nil || got != want {
			t.Errorf("ParseApprove(%q) = %q, %v", in, got, err)
		}
	}
	if _, err := ParseApprove("sometimes"); err == nil {
		t.Error("bad approve mode accepted")
	}
}

func TestCast(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rec", "a.cast")
	c, err := NewCast(path, 100, 30, "test")
	if err != nil {
		t.Fatal(err)
	}
	c.Output([]byte("hello "))
	euro := []byte("€") // three bytes, split across two writes
	c.Output(euro[:1])
	c.Output(append(euro[1:], '\n'))
	c.Input([]byte("q"))
	c.Resize(120, 40)
	c.Close("exit 0")
	c.Output([]byte("ignored after close"))

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if st, _ := f.Stat(); runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
	sc := bufio.NewScanner(f)
	sc.Scan()
	var hdr map[string]any
	if err := json.Unmarshal(sc.Bytes(), &hdr); err != nil || hdr["version"] != float64(2) || hdr["width"] != float64(100) {
		t.Fatalf("header %s: %v", sc.Bytes(), err)
	}
	var kinds, text []string
	for sc.Scan() {
		var ev []any
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil || len(ev) != 3 {
			t.Fatalf("event %s: %v", sc.Bytes(), err)
		}
		kinds = append(kinds, ev[1].(string))
		text = append(text, ev[2].(string))
	}
	if strings.Join(kinds, ",") != "o,o,i,r,m" {
		t.Errorf("kinds %v", kinds)
	}
	if text[1] != "€\n" || text[3] != "120x40" || text[4] != "exit 0" {
		t.Errorf("events %q", text)
	}
}
