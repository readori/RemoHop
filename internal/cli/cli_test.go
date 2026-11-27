package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestNormalizeTarget(t *testing.T) {
	cases := map[string]string{
		"123456789":      "123456789",
		"123 456 789":    "123456789",
		"123-456-789":    "123456789",
		"012345678":      "012345678",
		"devbox":         "devbox",
		"10.0.0.2:21118": "10.0.0.2:21118",
		"my-box":         "my-box",
	}
	for in, want := range cases {
		if got := NormalizeTarget(in); got != want {
			t.Errorf("NormalizeTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDispatch(t *testing.T) {
	var gotTarget string
	var gotArgs []string
	var gotGlobals Globals
	Register(&Command{Name: "t-remote", Remote: true, Run: func(ctx context.Context, env *Env, args []string) int {
		gotTarget, gotArgs, gotGlobals = env.Target, args, env.Globals
		return 7
	}})
	Register(&Command{Name: "t-local", Run: func(ctx context.Context, env *Env, args []string) int {
		gotArgs = args
		return 3
	}})

	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := Main(context.Background(), args, strings.NewReader(""), &out, &errb)
		return code, errb.String()
	}

	if code, _ := run("-v", "--password=pw", "--signal", "s:1", "123 456 789", "t-remote", "-c", "ls"); code != 7 {
		t.Fatalf("remote: code %d", code)
	}
	if gotTarget != "123456789" || strings.Join(gotArgs, " ") != "-c ls" {
		t.Fatalf("remote: target=%q args=%q", gotTarget, gotArgs)
	}
	if !gotGlobals.Verbose || gotGlobals.Password != "pw" || gotGlobals.Signal != "s:1" {
		t.Fatalf("globals: %+v", gotGlobals)
	}
	if code, _ := run("t-local", "a"); code != 3 || len(gotArgs) != 1 {
		t.Fatalf("local: code %d args %q", code, gotArgs)
	}
	if code, msg := run("t-remote"); code != ExitUsage || !strings.Contains(msg, "needs a target") {
		t.Fatalf("remote without target: %d %q", code, msg)
	}
	if code, msg := run("123456789", "t-local"); code != ExitUsage || !strings.Contains(msg, "does not take a target") {
		t.Fatalf("local with target: %d %q", code, msg)
	}
	if code, _ := run("123456789", "nope"); code != ExitUsage {
		t.Fatalf("unknown: %d", code)
	}
	if code, _ := run(); code != ExitUsage {
		t.Fatalf("empty: %d", code)
	}
}

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"plain text\twith tab\nand newline": "plain text\twith tab\nand newline",
		"中文 ok — é":                         "中文 ok — é",
		"\x1b]0;owned\a\x1b[2Jfake":         "?]0;owned??[2Jfake",
		"a\rb\x00c\x7f":                     "a?b?c?",
		"csi\u009b31m":                      "csi?31m",
		"rtl‮gnp.exe":                       "rtl?gnp.exe",
		"bad\xff\xfeutf8":                   "bad??utf8",
	}
	for in, want := range cases {
		if got := Sanitize(in); got != want {
			t.Errorf("Sanitize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestErrorfSanitizes(t *testing.T) {
	var errb bytes.Buffer
	env := &Env{Stderr: &errb}
	env.Errorf("login failed: %s", "\x1b[31mhi")
	if got := errb.String(); got != "remohop: login failed: ?[31mhi\n" {
		t.Fatalf("got %q", got)
	}
}

// TestHelpAndTargetOnly: `--help` after any command prints its help on
// stdout and exits 0; a host named alone (not on a terminal) lists what can
// be done with it; a word that is no host stays an unknown command.
func TestHelpAndTargetOnly(t *testing.T) {
	ran := false
	Register(&Command{Name: "t-help-remote", Remote: true, Summary: "remote thing", Usage: "Usage: remohop <ID> t-help-remote [-x]",
		Run: func(ctx context.Context, env *Env, args []string) int { ran = true; return 0 }})
	Register(&Command{Name: "t-help-local", Summary: "local thing",
		Run: func(ctx context.Context, env *Env, args []string) int { ran = true; return 0 }})
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := Main(context.Background(), args, strings.NewReader(""), &out, &errb)
		return code, out.String(), errb.String()
	}
	for _, args := range [][]string{
		{"t-help-local", "--help"}, {"t-help-local", "-v", "-h"}, {"t-help-remote", "--help"},
		{"123456789", "t-help-remote", "--help"}, {"10.0.0.2:21118", "t-help-remote", "-help"},
	} {
		code, out, errOut := run(args...)
		if code != ExitOK || out == "" || errOut != "" || ran {
			t.Errorf("%q: code %d, stdout %q, stderr %q, ran %v", args, code, out, errOut, ran)
		}
	}
	// After a positional argument or "--", --help belongs to the command.
	if code, _, _ := run("123456789", "t-help-remote", "ls", "--help"); code != 0 || !ran {
		t.Errorf("--help after an argument was taken: %d %v", code, ran)
	}
	if code, _, errOut := run("123456789"); code != ExitUsage || !strings.Contains(errOut, "t-help-remote") || strings.Contains(errOut, "unknown command") {
		t.Errorf("target alone: %d %q", code, errOut)
	}
	KnownTarget = func(n string) bool { return n == "lab" }
	defer func() { KnownTarget = nil }()
	if code, _, errOut := run("lab"); code != ExitUsage || !strings.Contains(errOut, "what should lab do") {
		t.Errorf("alias alone: %d %q", code, errOut)
	}
	if code, _, errOut := run("labx"); code != ExitUsage || !strings.Contains(errOut, "unknown command") {
		t.Errorf("unknown word: %d %q", code, errOut)
	}
	if !wantsHelp([]string{"--json", "--help"}) || wantsHelp([]string{"--", "--help"}) || wantsHelp([]string{"x", "-h"}) {
		t.Error("wantsHelp")
	}
}

// TestGlobalsAnywhere: -v, --signal and --password are taken after the
// target and after the command too, but not past "--", not inside a
// remote command line, and not from a command that defines them itself.
func TestGlobalsAnywhere(t *testing.T) {
	var gotArgs []string
	var gotGlobals Globals
	record := func(ctx context.Context, env *Env, args []string) int {
		gotArgs, gotGlobals = args, env.Globals
		return 0
	}
	Register(&Command{Name: "t-any", Remote: true, Run: record})
	Register(&Command{Name: "t-anylocal", Run: record})
	run := func(args ...string) (int, string) {
		gotArgs, gotGlobals = nil, Globals{}
		var out, errb bytes.Buffer
		code := Main(context.Background(), args, strings.NewReader(""), &out, &errb)
		return code, errb.String()
	}
	cases := []struct {
		args []string
		want string
		g    Globals
	}{
		{[]string{"123456789", "-v", "t-any", "a"}, "a", Globals{Verbose: true}},
		{[]string{"123456789", "--signal", "s:1", "t-any", "a"}, "a", Globals{Signal: "s:1"}},
		{[]string{"123456789", "t-any", "-x", "--password=pw", "a", "-v"}, "-x a", Globals{Verbose: true, Password: "pw"}},
		{[]string{"123456789", "t-any", "--signal", "s:2", "--", "-v"}, "-- -v", Globals{Signal: "s:2"}},
		{[]string{"t-anylocal", "--verbose", "b"}, "b", Globals{Verbose: true}},
	}
	for _, c := range cases {
		if code, msg := run(c.args...); code != 0 || strings.Join(gotArgs, " ") != c.want || gotGlobals.Verbose != c.g.Verbose ||
			gotGlobals.Signal != c.g.Signal || gotGlobals.Password != c.g.Password {
			t.Errorf("%q: code %d %q, args %q, globals %+v", c.args, code, msg, gotArgs, gotGlobals)
		}
	}
	if code, msg := run("123456789", "t-any", "--signal"); code != ExitUsage || !strings.Contains(msg, "needs a value") {
		t.Errorf("missing value: %d %q", code, msg)
	}
	// A remote command line keeps its flags.
	if got, _ := stripGlobals(&Globals{}, "shell", []string{"-v", "ls", "-v"}); strings.Join(got, " ") != "ls -v" {
		t.Errorf("shell: %q", got)
	}
	// A command's own flag of the same name stays its own.
	g := Globals{}
	if got, _ := stripGlobals(&g, "host", []string{"--password", "pw", "-v"}); strings.Join(got, " ") != "--password pw" || g.Password != "" || !g.Verbose {
		t.Errorf("host: %q %+v", got, g)
	}
}

// TestFlagErrorsOneLine: a bad flag is reported in one line that says
// where the full help is, not with the whole usage.
func TestFlagErrorsOneLine(t *testing.T) {
	var errb bytes.Buffer
	env := &Env{Stderr: &errb}
	fs := NewFlagSet(env, "net up", "Usage: remohop net up [--x]\n\nlong text")
	fs.Bool("x", false, "")
	if err := fs.Parse([]string{"--nope"}); err == nil {
		t.Fatal("no error")
	}
	got := errb.String()
	if strings.Count(got, "\n") != 1 || !strings.Contains(got, "-nope") || !strings.Contains(got, "see `remohop help net`") {
		t.Errorf("got %q", got)
	}
	errb.Reset()
	fs = NewFlagSet(env, "net up", "Usage: remohop net up [--x]\n\nlong text")
	if err := fs.Parse([]string{"-h"}); err == nil || !strings.Contains(errb.String(), "long text") {
		t.Errorf("-h: %v %q", err, errb.String())
	}
}

func TestHelpTopics(t *testing.T) {
	Register(&Command{Name: "t-topics", Usage: "Usage: short\n\nMore: remohop help t-topics deep", Topics: map[string]string{"deep": "DEEP PAGE"},
		Run: func(ctx context.Context, env *Env, args []string) int { return 0 }})
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := Main(context.Background(), args, strings.NewReader(""), &out, &errb)
		return code, out.String(), errb.String()
	}
	if code, out, _ := run("help", "t-topics"); code != 0 || strings.Contains(out, "DEEP") {
		t.Errorf("help: %d %q", code, out)
	}
	if code, out, _ := run("help", "t-topics", "deep"); code != 0 || strings.TrimSpace(out) != "DEEP PAGE" {
		t.Errorf("topic: %d %q", code, out)
	}
	for _, args := range [][]string{{"help", "t-topics", "all"}, {"t-topics", "--help-all"}} {
		if code, out, _ := run(args...); code != 0 || !strings.Contains(out, "Usage: short") || !strings.Contains(out, "DEEP PAGE") {
			t.Errorf("%q: %d %q", args, code, out)
		}
	}
	if code, _, msg := run("help", "t-topics", "nope"); code != ExitUsage || !strings.Contains(msg, "deep") {
		t.Errorf("unknown topic: %d %q", code, msg)
	}
}

func TestOneLine(t *testing.T) {
	for in, want := range map[string]string{
		"exec: \"git status\"":    "exec: \"git status\"",
		"touch x\n\n\ngit status": `touch x\n\n\ngit status`,
		"a\tb\r\x1b[2Jc":          `a\tb\r\x1b[2Jc`,
		"evil\u202etxt.exe":       `evil\u202etxt.exe`,
		"bad\xffbyte":             `bad\xffbyte`,
		"line\u2028sep":           `line\u2028sep`,
		"\u4e2d\u6587 \U0001f600": "\u4e2d\u6587 \U0001f600",
	} {
		if got := OneLine(in); got != want {
			t.Errorf("OneLine(%q) = %q, want %q", in, got, want)
		}
	}
}
