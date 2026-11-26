//go:build !windows

package desktop

import (
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestFilterEscape(t *testing.T) {
	for in, want := range map[string]string{
		"ipc:///tmp/remohop-kf-1/s":   `ipc\\:///tmp/remohop-kf-1/s`,
		`ipc://C:\Temp\a,b`:           `ipc\\://C\\:\\\\Temp\\\\a\,b`,
		"ipc:///tmp/it's [x];y/s":     `ipc\\:///tmp/it\\\'s \[x\]\;y/s`,
		"ipc:///var/folders/T/kf-1/s": `ipc\\:///var/folders/T/kf-1/s`,
	} {
		if got := filterEscape(in); got != want {
			t.Errorf("filterEscape(%q) = %s, want %s", in, got, want)
		}
	}
}

// fakeZMQ is the zmq filter's end of the socket: a REP socket that answers
// each command with reply(n, command), n counting connections from 0; an
// empty reply closes the connection instead.
func fakeZMQ(t *testing.T, path string, reply func(conn int, cmd string) string) <-chan string {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	cmds := make(chan string, 16)
	go func() {
		for n := 0; ; n++ {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			z := &zmtpConn{c: c}
			var g [64]byte
			if _, err := io.ReadFull(c, g[:]); err != nil {
				c.Close()
				continue
			}
			g[11] = 1 // ZMTP 3.1, as libzmq 4.3 answers
			c.Write(g[:])
			if _, _, err := z.readFrame(); err != nil {
				c.Close()
				continue
			}
			z.writeFrame(0x04, []byte("\x05READY\x0bSocket-Type\x00\x00\x00\x03REP"))
			for {
				var parts []string
				for {
					flags, body, err := z.readFrame()
					if err != nil {
						c.Close()
						goto next
					}
					parts = append(parts, string(body))
					if flags&0x01 == 0 {
						break
					}
				}
				cmd := parts[len(parts)-1]
				cmds <- cmd
				r := reply(n, cmd)
				if r == "" {
					c.Close()
					goto next
				}
				z.writeFrame(0x01, nil)
				z.writeFrame(0x00, []byte(r))
			}
		next:
		}
	}()
	return cmds
}

func nextCmd(t *testing.T, cmds <-chan string) string {
	t.Helper()
	select {
	case c := <-cmds:
		return c
	case <-time.After(5 * time.Second):
		t.Fatal("no command reached the filter")
		return ""
	}
}

// Each request switches the timestamps' parity; a connection that goes
// away (ffmpeg setting its filters up again) is made again, with the
// filters back at even timestamps, and the request in flight sent again.
func TestKeyForcerCommands(t *testing.T) {
	k, err := newKeyForcer()
	if err != nil {
		t.Fatal(err)
	}
	defer k.close()
	cmds := fakeZMQ(t, k.sock, func(conn int, cmd string) string {
		if conn == 0 && strings.HasSuffix(cmd, "+1") {
			return "" // the first connection breaks on the first command
		}
		return "0 Success"
	})
	k.start()
	odd, even := kfFilterName+" expr "+kfPTS(true), kfFilterName+" expr "+kfPTS(false)
	k.request()
	if c := nextCmd(t, cmds); c != odd {
		t.Fatalf("first command %q, want %q", c, odd)
	}
	if c := nextCmd(t, cmds); c != odd {
		t.Fatalf("after reconnecting: %q, want %q", c, odd)
	}
	start := time.Now()
	k.request()
	if c := nextCmd(t, cmds); c != even {
		t.Fatalf("second request: %q, want %q", c, even)
	}
	if d := time.Since(start); d < keyForceGap/2 {
		t.Errorf("second forced key frame after %v; they are spaced %v", d, keyForceGap)
	}
	if k.broken.Load() {
		t.Error("forcer broken after a reconnect")
	}
}

// A filter that refuses the command leaves the forcer broken, so the
// session restarts the encoder for key frames.
func TestKeyForcerRefused(t *testing.T) {
	k, err := newKeyForcer()
	if err != nil {
		t.Fatal(err)
	}
	defer k.close()
	cmds := fakeZMQ(t, k.sock, func(int, string) string { return "-38 Function not implemented" })
	k.start()
	k.request()
	nextCmd(t, cmds)
	for deadline := time.Now().Add(5 * time.Second); !k.broken.Load(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("forcer not broken after a refused command")
		}
	}
}
