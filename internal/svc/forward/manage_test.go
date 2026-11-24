package forward_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

type listRow struct {
	PID       int    `json:"pid"`
	ID        string `json:"id"`
	Direction string `json:"direction"`
	Proto     string `json:"proto"`
	Listen    string `json:"listen"`
	Target    string `json:"target"`
}

// call runs a command to completion; target "" uses the local form.
func call(t *testing.T, name, target string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	env := &cli.Env{Target: target, Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}
	c := cli.Lookup(name)
	run := c.Run
	if target != "" {
		run = c.RemoteRun
	}
	return run(context.Background(), env, args), out.String(), errb.String()
}

func listJSON(t *testing.T, target string) []listRow {
	t.Helper()
	code, out, errs := call(t, "forward", target, "--json")
	var rows []listRow
	if code != cli.ExitOK || json.Unmarshal([]byte(out), &rows) != nil {
		t.Fatalf("list: exit %d out %q err %q", code, out, errs)
	}
	return rows
}

func echoOK(addr string) bool {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	c.Write([]byte("ping"))
	c.(*net.TCPConn).CloseWrite()
	b, _ := io.ReadAll(c)
	return string(b) == "ping"
}

func TestListAndUnforward(t *testing.T) {
	client, _ := peertest.Pair(t)
	echo := echoServer(t)
	_, ep, _ := net.SplitHostPort(echo)

	if code, out, _ := call(t, "forward", ""); code != cli.ExitOK || !strings.Contains(out, "no forwards running") {
		t.Fatalf("empty list: %d %q", code, out)
	}
	if rows := listJSON(t, ""); len(rows) != 0 {
		t.Fatalf("rows %+v", rows)
	}

	env, out, errb := newEnv(client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runCmd(t, env, ctx, "0:"+ep, "0:"+ep, "-R", "0:"+ep, "--json")
	var lines []listenLine
	waitFor(t, "three listeners", func() bool {
		lines = lines[:0]
		sc := bufio.NewScanner(strings.NewReader(out.String()))
		for sc.Scan() {
			var l listenLine
			if json.Unmarshal(sc.Bytes(), &l) != nil {
				return false
			}
			lines = append(lines, l)
		}
		return len(lines) == 3
	})
	var rows []listRow
	waitFor(t, "forwards listed", func() bool { rows = listJSON(t, ""); return len(rows) == 3 })
	for i, r := range rows {
		if r.Listen != lines[i].Listen || r.Target != echo || r.ID != "test" || r.PID == 0 || r.Proto != "tcp" {
			t.Fatalf("row %d: %+v (want listen %s)", i, r, lines[i].Listen)
		}
	}
	if rows[0].Direction != "local" || rows[2].Direction != "reverse" {
		t.Fatalf("directions %+v", rows)
	}
	// Filtered by target, and the text form.
	if got := listJSON(t, "test"); len(got) != 3 {
		t.Fatalf("target list %+v", got)
	}
	if got := listJSON(t, "someone-else"); len(got) != 0 {
		t.Fatalf("other target list %+v", got)
	}
	code, text, _ := call(t, "forward", "test")
	if code != cli.ExitOK || !strings.Contains(text, lines[0].Listen+" -> "+echo) || !strings.Contains(text, "reverse") || !strings.Contains(text, "PID") {
		t.Fatalf("text list %d %q", code, text)
	}

	// Stop one local port: only that listener goes.
	_, p0, _ := net.SplitHostPort(lines[0].Listen)
	if code, o, e := call(t, "unforward", "someone-else", p0); code != cli.ExitError || !strings.Contains(e, "no running forward") {
		t.Fatalf("unforward for another target: %d %q %q", code, o, e)
	}
	code, o, e := call(t, "unforward", "", p0)
	if code != cli.ExitOK || !strings.Contains(o, "stopped "+lines[0].Listen+" -> "+echo) {
		t.Fatalf("unforward: %d %q %q", code, o, e)
	}
	if echoOK(lines[0].Listen) {
		t.Fatal("listener still answers after unforward")
	}
	if !echoOK(lines[1].Listen) || !echoOK(lines[2].Listen) {
		t.Fatal("the other forwards should keep running")
	}
	if got := listJSON(t, ""); len(got) != 2 || got[0].Listen != lines[1].Listen {
		t.Fatalf("list after unforward %+v", got)
	}
	select {
	case code := <-done:
		t.Fatalf("command exited (%d, %q) although forwards remain", code, errb.String())
	default:
	}
	// The same port again is an error; bad arguments are usage errors.
	if code, _, _ := call(t, "unforward", "", p0); code != cli.ExitError {
		t.Fatalf("second unforward: %d", code)
	}
	for _, args := range [][]string{nil, {"x"}, {"0"}, {"70000"}, {"--nope"}} {
		if code, _, _ := call(t, "unforward", "", args...); code != cli.ExitUsage {
			t.Errorf("unforward %v: exit %d", args, code)
		}
	}
	// `remohop forward SPEC` without a target is refused with a hint.
	if code, _, e := call(t, "forward", "", "1:2"); code != cli.ExitUsage || !strings.Contains(e, "needs a target") {
		t.Fatalf("local start: %d %q", code, e)
	}

	// Stopping the last local listener with --json; the reverse one remains,
	// so the process keeps running until cancelled.
	_, p1, _ := net.SplitHostPort(lines[1].Listen)
	code, o, _ = call(t, "unforward", "test", "--json", p1)
	var res struct {
		PID    int    `json:"pid"`
		Listen string `json:"listen"`
	}
	if code != cli.ExitOK || json.Unmarshal([]byte(o), &res) != nil || res.Listen != lines[1].Listen || res.PID != rows[0].PID {
		t.Fatalf("unforward json: %d %q", code, o)
	}
	cancel()
	if code := <-done; code != cli.ExitOK {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if rows := listJSON(t, ""); len(rows) != 0 {
		t.Fatalf("record left behind: %+v", rows)
	}
}

// Closing the only forward ends the command successfully.
func TestUnforwardLastListenerExits(t *testing.T) {
	client, _ := peertest.Pair(t)
	echo := echoServer(t)
	_, ep, _ := net.SplitHostPort(echo)
	env, out, errb := newEnv(client)
	done := runCmd(t, env, context.Background(), "0:"+ep)
	var addr string
	waitFor(t, "listening", func() bool {
		_, err := fmt.Sscanf(out.String(), "forwarding %s ->", &addr)
		return err == nil
	})
	_, port, _ := net.SplitHostPort(addr)
	waitFor(t, "forward listed", func() bool { return len(listJSON(t, "")) == 1 })
	if code, o, e := call(t, "unforward", "", port); code != cli.ExitOK {
		t.Fatalf("unforward: %d %q %q", code, o, e)
	}
	select {
	case code := <-done:
		if code != cli.ExitOK {
			t.Fatalf("exit %d: %s", code, errb.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("command kept running with no forwards left")
	}
}

func TestCLIUDPForward(t *testing.T) {
	client, _ := peertest.Pair(t)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 2048)
		for {
			n, src, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			pc.WriteTo(buf[:n], src)
		}
	}()
	_, port, _ := net.SplitHostPort(pc.LocalAddr().String())

	env, out, errb := newEnv(client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runCmd(t, env, ctx, "--udp", "--json", "0:"+port)
	var l struct{ Proto, Listen, Target string }
	waitFor(t, "udp listener reported", func() bool {
		return json.Unmarshal([]byte(strings.TrimSpace(out.String())), &l) == nil && l.Listen != ""
	})
	if l.Proto != "udp" || l.Target != pc.LocalAddr().String() {
		t.Fatalf("line %+v", l)
	}
	c, err := net.Dial("udp", l.Listen)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.Write([]byte("udp ping"))
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 64)
	n, err := c.Read(buf)
	if err != nil || string(buf[:n]) != "udp ping" {
		t.Fatalf("echo %q %v", buf[:n], err)
	}
	waitFor(t, "udp forward listed", func() bool {
		rows := listJSON(t, "test")
		return len(rows) == 1 && rows[0].Proto == "udp" && rows[0].Listen == l.Listen
	})
	// unforward works for UDP listeners too and ends the command.
	_, lp, _ := net.SplitHostPort(l.Listen)
	if code, o, e := call(t, "unforward", "", lp); code != cli.ExitOK {
		t.Fatalf("unforward: %d %q %q", code, o, e)
	}
	select {
	case code := <-done:
		if code != cli.ExitOK {
			t.Fatalf("exit %d: %s", code, errb.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("command kept running")
	}
}
