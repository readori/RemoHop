package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

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

// runMCP drives `remohop mcp <args>` through cli.Main with raw JSON-RPC lines on
// stdin, closes stdin, and returns the exit code and what was written.
func runMCP(t *testing.T, args []string, lines ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	old := newDial
	newDial = func(cli.Globals) DialFunc {
		return func(context.Context, string) (*peer.Conn, error) {
			client, _ := peertest.Pair(t)
			return client, nil
		}
	}
	t.Cleanup(func() { newDial = old })

	pr, pw := io.Pipe()
	var out, errb lockedBuf
	done := make(chan int, 1)
	go func() {
		done <- cli.Main(context.Background(), append([]string{"mcp"}, args...), pr, &out, &errb)
	}()
	for _, l := range lines {
		if _, err := io.WriteString(pw, l+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	// Let the server answer every request before the input ends.
	want := 0
	for _, l := range lines {
		if strings.Contains(l, `"id"`) {
			want++
		}
	}
	deadline := time.Now().Add(20 * time.Second)
	for strings.Count(out.String(), `"result"`)+strings.Count(out.String(), `"error"`) < want && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	pw.Close()
	select {
	case code = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("remohop mcp did not exit after stdin closed")
	}
	return code, out.String(), errb.String()
}

type rpcMsg struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func parseStdout(t *testing.T, stdout string) map[string]rpcMsg {
	t.Helper()
	byID := map[string]rpcMsg{}
	for _, line := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		var m rpcMsg
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("stdout line is not JSON: %q (%v)", line, err)
		}
		if m.JSONRPC != "2.0" {
			t.Fatalf("stdout line is not JSON-RPC 2.0: %q", line)
		}
		if m.Method == "" && m.ID == nil {
			t.Fatalf("stdout line is neither a request, notification nor response: %q", line)
		}
		if m.Method == "" {
			byID[string(m.ID)] = m
		}
	}
	return byID
}

func TestStdioStdoutIsOnlyProtocol(t *testing.T) {
	code, stdout, stderr := runMCP(t, []string{"--target", "123456789"},
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"raw","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"remohop_exec","arguments":{"command":%q}}}`, sh.outAndErr("from-remote", "to-stderr")),
		`{"jsonrpc":"2.0","id":4,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":5,"method":"no/such/method"}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"remohop_nope","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"remohop_info","arguments":{"target":"123456789"}}}`,
	)
	if code != cli.ExitOK {
		t.Fatalf("exit code %d, stderr: %s", code, stderr)
	}
	msgs := parseStdout(t, stdout)

	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		ServerInfo      struct{ Name string }
		Instructions    string
	}
	if err := json.Unmarshal(msgs["1"].Result, &init); err != nil || init.ProtocolVersion != "2024-11-05" || init.ServerInfo.Name != "remohop" {
		t.Fatalf("initialize: %s (%v)", msgs["1"].Result, err)
	}
	var list struct{ Tools []struct{ Name string } }
	if err := json.Unmarshal(msgs["2"].Result, &list); err != nil || len(list.Tools) != len(wantTools) {
		t.Fatalf("tools/list: %s (%v)", msgs["2"].Result, err)
	}
	var call struct {
		Content           []struct{ Type, Text string }
		StructuredContent struct{ Stdout, Stderr string }
		IsError           bool
	}
	if err := json.Unmarshal(msgs["3"].Result, &call); err != nil || call.IsError || call.StructuredContent.Stdout != "from-remote\n" || call.StructuredContent.Stderr != "to-stderr\n" {
		t.Fatalf("exec result: %s (%v)", msgs["3"].Result, err)
	}
	if msgs["4"].Result == nil || msgs["4"].Error != nil {
		t.Fatalf("ping: %+v", msgs["4"])
	}
	if msgs["5"].Error == nil || msgs["5"].Error.Code != -32601 {
		t.Fatalf("unknown method should be -32601: %+v", msgs["5"])
	}
	if msgs["6"].Error == nil {
		t.Fatalf("unknown tool should be a protocol error: %+v", msgs["6"])
	}
	if msgs["7"].Error != nil {
		t.Fatalf("info: %+v", msgs["7"])
	}
	if strings.Contains(stdout, "to-stderr") && !strings.Contains(string(msgs["3"].Result), "to-stderr") {
		t.Error("remote stderr leaked outside the result")
	}
}

func TestProtocolVersionNegotiation(t *testing.T) {
	_, stdout, _ := runMCP(t, nil,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01","capabilities":{},"clientInfo":{"name":"raw","version":"0"}}}`)
	msgs := parseStdout(t, stdout)
	var init struct{ ProtocolVersion string }
	if err := json.Unmarshal(msgs["1"].Result, &init); err != nil || init.ProtocolVersion == "" || init.ProtocolVersion == "1999-01-01" {
		t.Fatalf("server should answer an unknown version with one it supports: %s (%v)", msgs["1"].Result, err)
	}
}

func TestMissingTargetOverStdio(t *testing.T) {
	// No --target and no argument: an isError result that names the fix.
	_, stdout, _ := runMCP(t, nil,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"raw","version":"0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"remohop_info","arguments":{}}}`)
	msgs := parseStdout(t, stdout)
	var call struct {
		IsError bool
		Content []struct{ Text string }
	}
	if err := json.Unmarshal(msgs["2"].Result, &call); err != nil || !call.IsError || !strings.Contains(call.Content[0].Text, "target") {
		t.Fatalf("got %s", msgs["2"].Result)
	}
}

func TestCommandFlags(t *testing.T) {
	var out, errb bytes.Buffer
	env := &cli.Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}
	if code := run(context.Background(), env, []string{"--bogus"}); code != cli.ExitUsage {
		t.Errorf("bad flag: exit %d", code)
	}
	if code := run(context.Background(), env, []string{"extra"}); code != cli.ExitUsage {
		t.Errorf("extra arg: exit %d", code)
	}
	if code := run(context.Background(), env, []string{"--idle-timeout", "-1s"}); code != cli.ExitUsage {
		t.Errorf("negative idle: exit %d", code)
	}
	// stdin at EOF: the server shuts down cleanly and writes nothing.
	out.Reset()
	if code := run(context.Background(), env, nil); code != cli.ExitOK || out.Len() != 0 {
		t.Errorf("empty stdin: exit %d, stdout %q", code, out.String())
	}
	var help bytes.Buffer
	cli.Main(context.Background(), []string{"help", "mcp"}, nil, &help, io.Discard)
	for _, want := range []string{"--target", "claude mcp add remohop -- remohop mcp", "mcpServers", "remohop_screenshot"} {
		if !strings.Contains(help.String(), want) {
			t.Errorf("usage lacks %q", want)
		}
	}
	if fmt.Sprint(cli.Lookup("mcp").Remote) != "false" {
		t.Error("mcp must be a local command")
	}
}

func TestSanitize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain\ttext\n", "plain\ttext\n"},
		{"\x1b[1;31mred\x1b[0m", "red"},
		{"a\x1b]8;;http://evil\x07link\x1b]8;;\x07b", "alinkb"},
		{"a\x1b]0;title\x1b\\b", "ab"},
		{"x\x1b[2Ky", "xy"},
		{"cr\r\nlf\rbare", "cr\nlf\nbare"},
		{"nul\x00bel\x07del\x7f", "nulbeldel"},
		{"bidi ‮evil‬", "bidi evil"},
		{"bad\xffutf8", "bad�utf8"},
		{"c1\u0085ok", "c1ok"},
		{"héllo 世界 🎉", "héllo 世界 🎉"},
	}
	for _, c := range cases {
		if got := clean(c.in); got != c.want {
			t.Errorf("clean(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got, changed := sanitize("a\r\nb\x1b[0m", true); got != "a\r\nb" || !changed {
		t.Errorf("keepCR: %q %v", got, changed)
	}
	if got := truncUTF8("aé€", 2); got != "a" {
		t.Errorf("truncUTF8 split a rune: %q", got)
	}
	v := struct {
		A string
		B []struct{ C string }
		d string
	}{A: "\x1b[1mx", B: []struct{ C string }{{"y\x07"}}}
	cleanStrings(&v)
	if v.A != "x" || v.B[0].C != "y" {
		t.Errorf("cleanStrings: %+v", v)
	}
}

func TestTrimPartialRune(t *testing.T) {
	for in, want := range map[string]string{
		"abc":           "abc",
		"a\xc3":         "a",
		"a\xe2\x82":     "a",
		"a\xe2\x82\xac": "a€",
		"":              "",
	} {
		if got := string(trimPartialRune([]byte(in))); got != want {
			t.Errorf("trimPartialRune(%q) = %q, want %q", in, got, want)
		}
	}
}
