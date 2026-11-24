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
