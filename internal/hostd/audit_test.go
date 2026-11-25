package hostd

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A flood of refused logins, from one source or from many, cannot push the
// records out of the audit log, and is summarised instead of written line by
// line.
func TestAuditRefusedFlood(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	a := &auditLog{path: filepath.Join(dir, AuditFile), now: func() time.Time { return now }}
	a.add("login", "result", "accepted", "name", "alice")

	src := netip.MustParseAddrPort("192.0.2.7:4000")
	for i := range 5000 {
		a.addRefused(sourceKey(src), "login", "result", "refused", "reason", "locked_out", "from", fmt.Sprintf("192.0.2.7:%d", 4000+i))
	}
	// Many sources, including IPv6 addresses of one /64 that count as one.
	for i := range 5000 {
		ap := netip.AddrPortFrom(netip.MustParseAddr(fmt.Sprintf("2001:db8::%x", i+1)), 1)
		a.addRefused(sourceKey(ap), "login", "result", "refused", "reason", "auth_failed", "from", ap.String())
	}
	for i := range 5000 {
		ap := netip.AddrPortFrom(netip.AddrFrom4([4]byte{198, 51, byte(i >> 8), byte(i)}), 1)
		a.addRefused(sourceKey(ap), "login", "result", "refused", "reason", "auth_failed", "from", ap.String())
	}
	now = now.Add(refusedWindow)
	a.flush()

	entries, err := ReadAudit(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	var full, summaries, suppressed int
	accepted := false
	for _, e := range entries {
		if e.Fields["result"] == "accepted" {
			accepted = e.Fields["name"] == "alice"
			continue
		}
		if n := e.Fields["suppressed"]; n != "" {
			summaries++
			var k int
			fmt.Sscan(n, &k)
			suppressed += k
			continue
		}
		full++
	}
	if !accepted {
		t.Fatal("the accepted login is gone")
	}
	if full != refusedTotal {
		t.Fatalf("%d refused logins written in full, want %d", full, refusedTotal)
	}
	if full+suppressed != 15000 {
		t.Fatalf("%d written + %d suppressed, want 15000", full, suppressed)
	}
	if summaries > refusedKeys+1 {
		t.Fatalf("%d summaries", summaries)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, AuditFile))
	if strings.Count(string(raw), "\n") != 1 {
		t.Fatalf("refused logins in the records file:\n%s", raw)
	}

	// A new window writes in full again.
	a.addRefused(sourceKey(src), "login", "result", "refused", "reason", "auth_failed")
	if e, _ := ReadAudit(dir, 1); len(e) != 1 || e[0].Fields["suppressed"] != "" || e[0].Fields["reason"] != "auth_failed" {
		t.Fatalf("after the window: %+v", e)
	}
}

// The records log keeps several rotated files, and the refused log rotates
// on its own.
func TestAuditRotation(t *testing.T) {
	dir := t.TempDir()
	a := &auditLog{path: filepath.Join(dir, AuditFile)}
	a.add("login", "result", "accepted", "name", "first")
	big := strings.Repeat("x", auditField)
	for range 3 * auditMax / 250 {
		a.add("grant-action", "kind", "file", "path", big)
	}
	entries, err := ReadAudit(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Fields["name"] != "first" {
		t.Fatalf("the first record was rotated out after 3 MB: %+v", entries[0])
	}
	for i := 1; i <= 3; i++ {
		if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("%s.%d", AuditFile, i))); err != nil {
			t.Fatalf("rotation %d: %v", i, err)
		}
	}
}

// The host remembers the ID each rendezvous server gave it, so that a
// change (an ID moved between devices) is recorded across restarts.
func TestNoteID(t *testing.T) {
	dir := t.TempDir()
	if prev := noteID(dir, "sig.example:21116", "123456789"); prev != "" {
		t.Fatalf("first ID: previous %q", prev)
	}
	if prev := noteID(dir, "sig.example:21116", "123456789"); prev != "" {
		t.Fatalf("same ID: previous %q", prev)
	}
	if prev := noteID(dir, "other.example:21116", "555555555"); prev != "" {
		t.Fatalf("another server: previous %q", prev)
	}
	if prev := noteID(dir, "sig.example:21116", "987654321"); prev != "123456789" {
		t.Fatalf("changed ID: previous %q", prev)
	}
}
