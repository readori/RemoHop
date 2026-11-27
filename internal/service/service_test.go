package service

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/readori/RemoHop/internal/cli"
)

const wantUserUnit = `# Written by ` + "`remohop service install`" + `. Customise with ` + "`systemctl edit remohop.service`" + `.
[Unit]
Description=RemoHop host (remote control)
Documentation=https://github.com/readori/RemoHop

[Service]
Type=simple
ExecStart=/usr/local/bin/remohop host --signal rendezvous.example.net:21116
Restart=on-failure
RestartSec=5
KillMode=process

[Install]
WantedBy=default.target
`

const wantSystemUnit = `# Written by ` + "`remohop service install`" + `. Customise with ` + "`systemctl edit remohop.service`" + `.
[Unit]
Description=RemoHop host (remote control)
Documentation=https://github.com/readori/RemoHop
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart="/opt/my apps/remohop" host
Restart=on-failure
RestartSec=5
KillMode=process
Environment=REMOHOP_CONFIG_DIR=/var/lib/remohop
StateDirectory=remohop
StateDirectoryMode=0700
Environment=HOME=/root

[Install]
WantedBy=multi-user.target
`

func TestSystemdUnitGolden(t *testing.T) {
	b := &systemd{}
	got := b.text(ScopeUser, Options{Signal: "rendezvous.example.net:21116"}, "/usr/local/bin/remohop")
	if got != wantUserUnit {
		t.Errorf("user unit:\n%s\nwant:\n%s", got, wantUserUnit)
	}
	got = b.text(ScopeSystem, Options{}, "/opt/my apps/remohop")
	if got != wantSystemUnit {
		t.Errorf("system unit:\n%s\nwant:\n%s", got, wantSystemUnit)
	}
	got = b.text(ScopeSystem, Options{Name: "lab", RunAs: "svc-remohop"}, "/usr/bin/remohop")
	for _, want := range []string{"Environment=REMOHOP_CONFIG_DIR=/var/lib/lab", "StateDirectory=lab", "User=svc-remohop"} {
		if !strings.Contains(got, want) {
			t.Errorf("named unit lacks %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "HOME=/root") {
		t.Error("HOME=/root set although a user is configured")
	}
}

func TestConfigDirAndNoSignal(t *testing.T) {
	o := Options{NoSignal: true, ConfigDir: "/srv/so desk"}
	unit := (&systemd{}).text(ScopeUser, o, "/usr/bin/remohop")
	for _, w := range []string{"ExecStart=/usr/bin/remohop host --no-signal\n", `Environment="REMOHOP_CONFIG_DIR=/srv/so desk"` + "\n"} {
		if !strings.Contains(unit, w) {
			t.Errorf("unit lacks %q:\n%s", w, unit)
		}
	}
	if strings.Contains(unit, "StateDirectory") {
		t.Error("StateDirectory set for a custom config dir")
	}
	plist := (&launchd{home: "/Users/ann", euid: 501}).text(ScopeUser, o, "/usr/local/bin/remohop")
	for _, w := range []string{"<string>--no-signal</string>", "<key>REMOHOP_CONFIG_DIR</key>\n\t\t<string>/srv/so desk</string>"} {
		if !strings.Contains(plist, w) {
			t.Errorf("plist lacks %q:\n%s", w, plist)
		}
	}
	if (Options{NoSignal: true, Signal: "x.example"}).validate() == nil {
		t.Error("--signal with --no-signal accepted")
	}
	if (Options{ConfigDir: "rel"}).validate() == nil {
		t.Error("relative --config-dir accepted")
	}
}

func TestSystemdQuote(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/bin/remohop": "/usr/bin/remohop",
		"/a b/remohop":     `"/a b/remohop"`,
		`/a"b`:             `"/a\"b"`,
		"/100%/remohop":    `"/100%%/remohop"`,
		"/x/$HOME/remohop": `"/x/$$HOME/remohop"`,
		"":                 `""`,
		`/back\slash`:      `"/back\\slash"`,
	} {
		if got := quote(in); got != want {
			t.Errorf("quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestLaunchdPlistGolden(t *testing.T) {
	b := &launchd{home: "/Users/ann", euid: 501}
	got := b.text(ScopeUser, Options{Signal: "rv.example.net"}, "/usr/local/bin/remohop")
	want := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.remohop.host</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/bin/remohop</string>
		<string>host</string>
		<string>--signal</string>
		<string>rv.example.net</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>ThrottleInterval</key>
	<integer>10</integer>
	<key>AbandonProcessGroup</key>
	<true/>
	<key>StandardOutPath</key>
	<string>/Users/ann/Library/Logs/remohop/remohop.log</string>
	<key>StandardErrorPath</key>
	<string>/Users/ann/Library/Logs/remohop/remohop.log</string>
	<key>EnvironmentVariables</key>
	<dict>
		<key>PATH</key>
		<string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
	</dict>
</dict>
</plist>
`
	if got != want {
		t.Errorf("plist:\n%s\nwant:\n%s", got, want)
	}
	sys := b.text(ScopeSystem, Options{}, "/opt/a&b/remohop")
	for _, w := range []string{"<string>/opt/a&amp;b/remohop</string>", "REMOHOP_CONFIG_DIR", "/Library/Application Support/remohop", "/var/log/remohop.log"} {
		if !strings.Contains(sys, w) {
			t.Errorf("system plist lacks %q", w)
		}
	}
	if b.plistPath(ScopeUser, Options{}) != "/Users/ann/Library/LaunchAgents/com.remohop.host.plist" {
		t.Error("plist path")
	}
	if b.label(Options{Name: "lab"}) != "com.remohop.lab" || b.domain(ScopeUser) != "gui/501" || b.domain(ScopeSystem) != "system" {
		t.Error("label/domain")
	}
}

func TestParsePrint(t *testing.T) {
	out := "com.remohop.host = {\n\tactive count = 1\n\tstate = running\n\tprogram = /x\n\tpid = 4242\n\tevent triggers = {\n\t}\n}\n"
	if s, p := parsePrint(out); s != "running" || p != 4242 {
		t.Errorf("got %q %d", s, p)
	}
}

func TestValidate(t *testing.T) {
	bad := []Options{
		{Name: "a b"}, {Name: "../x"}, {Name: "-x"}, {Name: strings.Repeat("a", 41)},
		{Signal: "x\nExecStart=/bin/sh"}, {Signal: "a b"}, {Signal: "-evil"},
		{RunAs: "Root"}, {RunAs: "a b"}, {Exe: "relative/remohop"}, {Exe: "/x\n/remohop"},
	}
	for _, o := range bad {
		if o.validate() == nil {
			t.Errorf("%+v accepted", o)
		}
	}
	good := Options{Name: "lab-1.x", Signal: "[::1]:21116", RunAs: "remohop", Exe: "/usr/bin/remohop"}
	if err := good.validate(); err != nil {
		t.Error(err)
	}
}

func TestLastLines(t *testing.T) {
	in := []byte("a\nb\nc\nd\n")
	if got := string(lastLines(in, 2, false)); got != "c\nd\n" {
		t.Errorf("got %q", got)
	}
	if got := string(lastLines(in, 10, false)); got != "a\nb\nc\nd\n" {
		t.Errorf("got %q", got)
	}
	if got := string(lastLines([]byte("xx\nb\nc\n"), 10, true)); got != "b\nc\n" {
		t.Errorf("partial: %q", got)
	}
	if lastLines(in, 0, false) != nil {
		t.Error("n=0")
	}
}

func TestTailFileSanitizes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.log")
	os.WriteFile(p, []byte("one\n\x1b]52;c;evil\x07two\nthree"), 0o600)
	var out bytes.Buffer
	if err := tailFile(context.Background(), p, 2, false, &out); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(out.String(), "\x1b\x07") || !strings.HasSuffix(out.String(), "three\n") || strings.Contains(out.String(), "one") {
		t.Errorf("got %q", out.String())
	}
}

func runCLI(t *testing.T, b backend, args ...string) (int, string, string) {
	t.Helper()
	old := newBackend
	newBackend = func() (backend, error) { return b, nil }
	t.Cleanup(func() { newBackend = old })
	var out, errb bytes.Buffer
	env := &cli.Env{Stdout: &out, Stderr: &errb, Stdin: strings.NewReader("")}
	code := cli.Lookup("service").Run(context.Background(), env, args)
	return code, out.String(), errb.String()
}

func TestCLIPrintAndUsage(t *testing.T) {
	b := &systemd{euid: 1000}
	code, out, _ := runCLI(t, b, "print", "--exe", "/usr/bin/remohop", "--signal", "s.example:1")
	if code != 0 || !strings.Contains(out, "ExecStart=/usr/bin/remohop host --signal s.example:1") {
		t.Errorf("%d %s", code, out)
	}
	for _, args := range [][]string{{}, {"bogus"}, {"install", "--user", "--system"}, {"install", "extra"}, {"install", "--name", "a b"}, {"logs", "-n", "-1"}} {
		if code, _, _ := runCLI(t, b, args...); code != cli.ExitUsage {
			t.Errorf("%v: code %d, want usage", args, code)
		}
	}
}

func TestStatusJSONNotInstalled(t *testing.T) {
	b := &systemd{home: t.TempDir(), systemDir: t.TempDir(), euid: 1000}
	code, out, _ := runCLI(t, b, "status", "--json")
	var st Status
	if err := json.Unmarshal([]byte(out), &st); err != nil || st.Installed || code != cli.ExitError {
		t.Errorf("%d %q %v", code, out, err)
	}
	if code, out, _ := runCLI(t, b, "uninstall"); code != 0 || !strings.Contains(out, "not installed") {
		t.Errorf("uninstall of nothing: %d %q", code, out)
	}
	if code, _, e := runCLI(t, b, "start"); code != cli.ExitError || !strings.Contains(e, "not installed") {
		t.Errorf("start of nothing: %d %q", code, e)
	}
}

func TestDefaultRunHost(t *testing.T) {
	var gotArgs []string
	cli.Register(&cli.Command{Name: "host", Run: func(ctx context.Context, env *cli.Env, args []string) int {
		gotArgs = args
		env.Errorf("hello")
		<-ctx.Done()
		return cli.ExitOK
	}})
	ctx, cancel := context.WithCancel(context.Background())
	var log bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- RunHost(ctx, []string{"--signal", "x"}, &log) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if strings.Join(gotArgs, " ") != "--signal x" || !strings.Contains(log.String(), "hello") {
		t.Errorf("args %v log %q", gotArgs, log.String())
	}
}

// TestStatusSuggestsRole: a missing signal service is installed with
// --role signal, so that is what status suggests.
func TestStatusSuggestsRole(t *testing.T) {
	var out strings.Builder
	printStatus(&out, &Status{}, true)
	if !strings.Contains(out.String(), "remohop service install --role signal") {
		t.Errorf("signal: %q", out.String())
	}
	out.Reset()
	printStatus(&out, &Status{}, false)
	if strings.Contains(out.String(), "--role") || !strings.Contains(out.String(), "remohop service install") {
		t.Errorf("host: %q", out.String())
	}
}
