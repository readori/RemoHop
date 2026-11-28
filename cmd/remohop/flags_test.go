package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/readori/RemoHop/internal/cli"
)

// TestFlagErrorsOneLine: an unknown flag is reported in one line that says
// where the full help is, never with the command's whole usage. None of
// these reach the network: flags are parsed first.
func TestFlagErrorsOneLine(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	for _, args := range [][]string{
		{"host", "--bogus"},
		{"host", "status", "--bogus"},
		{"host", "sessions", "--bogus"},
		{"host", "paired", "--bogus"},
		{"host", "authorize", "--bogus"},
		{"host", "grant", "--bogus"},
		{"host", "grants", "--bogus"},
		{"host", "panic", "--bogus"},
		{"host", "audit", "--bogus"},
		{"status", "--bogus"},
		{"log", "--bogus"},
		{"umount", "--bogus"},
		{"clip", "history", "--bogus"},
		{"123456789", "cp", "--bogus", "a", ":b"},
		{"123456789", "sync", "--bogus", "a", ":b"},
		{"123456789", "ls", "--bogus"},
		{"123456789", "send", "--bogus", "a"},
		{"123456789", "mount", "--bogus", ":/", "m"},
		{"123456789", "shell", "--bogus"},
		{"123456789", "spawn", "--bogus", "x"},
		{"123456789", "clip", "get", "--bogus"},
	} {
		var out, errb bytes.Buffer
		code := cli.Main(context.Background(), args, strings.NewReader(""), &out, &errb)
		cmd := args[0]
		if cli.LooksLikeTarget(cmd) {
			cmd = args[1]
		}
		msg := errb.String()
		if code != cli.ExitUsage || strings.Count(msg, "\n") != 1 || !strings.Contains(msg, "-bogus") ||
			!strings.Contains(msg, "see `remohop help "+cmd+"`") {
			t.Errorf("%q: exit %d, stderr %q", args, code, msg)
		}
	}
}
