//go:build unix

package hostd_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/hostd"
)

// With --approve each, the agent chooses the text of what the owner is
// asked about. Line breaks in a command must not push its start off the
// screen, and a bare "y" typed while two requests wait must not answer
// the older one: the owner, reading the latest prompt, means that one.
func TestApprovalPromptCannotBeSpoofed(t *testing.T) {
	var mu sync.Mutex
	var seen []hostd.Approval
	h, dir := ownedHost(t, hostd.Config{AskTimeout: 10 * time.Second, OnApproval: func(a hostd.Approval) {
		mu.Lock()
		seen = append(seen, a)
		mu.Unlock()
	}})
	conn, _, err := redeem(t, h, dir, hostd.GrantRequest{Name: "pair", For: time.Hour,
		Allow: []string{"shell:run-only"}, Approve: "each"})
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "pwned")
	evil := "touch " + marker + strings.Repeat("\n", 60) + "git status"
	waitN := func(n int) {
		t.Helper()
		for i := 0; i < 500; i++ {
			mu.Lock()
			k := len(seen)
			mu.Unlock()
			if k >= n {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("approval %d never asked", n)
	}
	evilDone := make(chan error, 1)
	go func() { _, err := run(t, conn, evil, false); evilDone <- err }()
	waitN(1)
	goodDone := make(chan error, 1)
	go func() { _, err := run(t, conn, "echo fine", false); goodDone <- err }()
	waitN(2)
	mu.Lock()
	first, second := seen[0], seen[1]
	mu.Unlock()

	// Shown on one line, all of it.
	shown := cli.OneLine(first.Action)
	if strings.ContainsAny(shown, "\n\r") || !strings.Contains(shown, "touch "+marker) || !strings.Contains(shown, `\n`) {
		t.Fatalf("the action as shown: %q", shown)
	}

	// The owner, looking at the latest prompt, types "y": refused, as
	// two requests wait; nothing is approved.
	if err := h.Decide(0, true); err == nil {
		t.Fatal("a bare answer was applied while two requests wait")
	}
	if msg := hostd.ApprovalAnswer(h, "y", second.N); !strings.Contains(msg, "y N") {
		t.Fatalf("bare y at the terminal: %q", msg)
	}
	if len(h.Pending()) != 2 {
		t.Fatalf("pending after a bare y: %d", len(h.Pending()))
	}
	// Named, it applies to that one, and the confirmation says which.
	msg := hostd.ApprovalAnswer(h, "y "+itoa(second.N), second.N)
	if !strings.Contains(msg, "approved request #"+itoa(second.N)) || !strings.Contains(msg, `exec: "echo fine"`) {
		t.Fatalf("confirmation: %q", msg)
	}
	if err := <-goodDone; err != nil {
		t.Fatalf("the approved command: %v", err)
	}
	// One left, and it is the one asked about last: a bare n answers it.
	if msg := hostd.ApprovalAnswer(h, "n", first.N); !strings.Contains(msg, "denied request #"+itoa(first.N)) {
		t.Fatalf("bare n with one request: %q", msg)
	}
	if err := <-evilDone; err == nil {
		t.Fatal("the denied command ran")
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the hidden command ran")
	}
}
