package clip

import (
	"flag"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite golden files")

func openTestHistory(t *testing.T) *History {
	t.Helper()
	h, err := OpenHistory(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func mustAdd(t *testing.T, h *History, e Entry, c Content) bool {
	t.Helper()
	ok, err := h.Add(e, c)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func TestHistoryRecordDedupeAndFilter(t *testing.T) {
	h := openTestHistory(t)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	at := func(m int) time.Time { return base.Add(time.Duration(m) * time.Minute) }

	if !mustAdd(t, h, Entry{Time: at(0), Dir: DirPush, Via: "set", Peer: "A"}, Text("one")) {
		t.Fatal("first add not recorded")
	}
	// Consecutive duplicate (same peer, direction, content): skipped.
	if mustAdd(t, h, Entry{Time: at(1), Dir: DirPush, Via: "sync", Peer: "A"}, Text("one")) {
		t.Fatal("consecutive duplicate recorded")
	}
	// Same content in the other direction, or for another peer, is a new fact.
	mustAdd(t, h, Entry{Time: at(2), Dir: DirPull, Via: "get", Peer: "A"}, Text("one"))
	mustAdd(t, h, Entry{Time: at(3), Dir: DirPull, Via: "get", Peer: "B"}, Text("two"))
	// A non-consecutive repeat is recorded.
	mustAdd(t, h, Entry{Time: at(4), Dir: DirPull, Via: "get", Peer: "A"}, Text("one"))
	if mustAdd(t, h, Entry{Dir: DirPull, Peer: "A"}, Content{Kind: KindText}) {
		t.Fatal("empty content recorded")
	}

	all, err := h.List(Filter{})
	if err != nil || len(all) != 4 {
		t.Fatalf("List = %d entries, %v", len(all), err)
	}
	if a, _ := h.List(Filter{Peer: "A"}); len(a) != 3 {
		t.Errorf("peer A has %d entries, want 3", len(a))
	}
	if n, _ := h.List(Filter{Limit: 2}); len(n) != 2 || n[1].Time != at(4) || n[0].Time != at(3) {
		t.Errorf("Limit 2 = %+v", n)
	}
	if s, _ := h.List(Filter{Since: at(3)}); len(s) != 2 {
		t.Errorf("Since = %d entries, want 2", len(s))
	}

	removed, err := h.Clear("A")
	if err != nil || removed != 3 {
		t.Fatalf("Clear(A) = %d, %v", removed, err)
	}
	if rest, _ := h.List(Filter{}); len(rest) != 1 || rest[0].Peer != "B" {
		t.Fatalf("after Clear(A): %+v", rest)
	}
	if removed, _ := h.Clear(""); removed != 1 {
		t.Fatalf("Clear all removed %d", removed)
	}
}

func TestHistoryPrunesByCountAndSize(t *testing.T) {
	h := openTestHistory(t)
	h.MaxEntries = 5
	for i := 0; i < 12; i++ {
		mustAdd(t, h, Entry{Dir: DirPush, Peer: "A", Time: time.Unix(int64(1000+i), 0)}, Text(strings.Repeat("x", i+1)))
	}
	es, _ := h.List(Filter{})
	if len(es) != 5 || es[0].Size != 8 || es[4].Size != 12 {
		t.Fatalf("count pruning kept %d entries (first size %d)", len(es), es[0].Size)
	}

	h = openTestHistory(t)
	h.MaxBytes = 1000
	for i := 0; i < 10; i++ {
		mustAdd(t, h, Entry{Dir: DirPush, Peer: "A", Time: time.Unix(int64(1000+i), 0)}, Text(strings.Repeat(string(rune('a'+i)), 300)))
	}
	es, _ = h.List(Filter{})
	if len(es) != 3 || es[2].Text[0] != 'j' {
		t.Fatalf("size pruning kept %d entries", len(es))
	}

	// A single item above the byte budget is still kept (the newest always is).
	mustAdd(t, h, Entry{Dir: DirPush, Peer: "A"}, Text(strings.Repeat("z", 5000)))
	es, _ = h.List(Filter{})
	if len(es) != 1 || es[0].Size != 5000 {
		t.Fatalf("oversize newest item: %d entries", len(es))
	}

	// Long text is cut for storage but flagged, and cut on a rune boundary.
	h = openTestHistory(t)
	h.MaxText = 10
	mustAdd(t, h, Entry{Dir: DirPush, Peer: "A"}, Text(strings.Repeat("é", 20)))
	es, _ = h.List(Filter{})
	if !es[0].Truncated || es[0].Size != 40 || len(es[0].Text) > 10 || strings.ContainsRune(es[0].Text, '\uFFFD') {
		t.Fatalf("truncation: %+v", es[0])
	}
}

func TestHistoryImagesStoredAndPruned(t *testing.T) {
	h := openTestHistory(t)
	h.MaxEntries = 2
	var files []string
	for i := 1; i <= 3; i++ {
		c, _ := PNG(testPNG(t, 4*i, 4, false))
		mustAdd(t, h, Entry{Dir: DirPull, Peer: "A", Time: time.Unix(int64(i), 0)}, c)
		es, _ := h.List(Filter{})
		files = append(files, es[len(es)-1].File)
	}
	es, _ := h.List(Filter{})
	if len(es) != 2 {
		t.Fatalf("%d entries", len(es))
	}
	if _, err := os.Stat(files[0]); !os.IsNotExist(err) {
		t.Errorf("pruned image file still exists: %v", err)
	}
	for _, f := range files[1:] {
		st, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && st.Mode().Perm() != 0o600 {
			t.Errorf("image file mode %v, want 0600", st.Mode().Perm())
		}
	}
	c, err := es[1].Content()
	if err != nil || c.Kind != KindImage || c.Hash() != es[1].Hash {
		t.Fatalf("Entry.Content: %v", err)
	}
	if runtime.GOOS != "windows" {
		st, _ := os.Stat(filepath.Join(h.Dir(), historyFile))
		if st.Mode().Perm() != 0o600 {
			t.Errorf("history file mode %v, want 0600", st.Mode().Perm())
		}
	}
	mustAdd(t, h, Entry{Dir: DirPull, Peer: "A"}, Text("t"))
	if _, err := os.Stat(es[0].File); !os.IsNotExist(err) {
		t.Errorf("image of pruned entry survived")
	}
}

func TestHistorySkipsCorruptLines(t *testing.T) {
	h := openTestHistory(t)
	mustAdd(t, h, Entry{Dir: DirPush, Peer: "A"}, Text("good"))
	f, _ := os.OpenFile(filepath.Join(h.Dir(), historyFile), os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString("{not json\n\n")
	f.Close()
	mustAdd(t, h, Entry{Dir: DirPush, Peer: "A"}, Text("also good"))
	es, err := h.List(Filter{})
	if err != nil || len(es) != 2 {
		t.Fatalf("List = %d, %v", len(es), err)
	}
}

func TestHistoryConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	done := make(chan struct{})
	for g := 0; g < 4; g++ {
		go func() {
			h, _ := OpenHistory(dir) // separate handles, like separate processes
			for i := 0; i < 15; i++ {
				h.Add(Entry{Dir: DirPush, Peer: "A"}, Text(strings.Repeat(string(rune('a'+g)), i+1)))
			}
			done <- struct{}{}
		}()
	}
	for g := 0; g < 4; g++ {
		<-done
	}
	h, _ := OpenHistory(dir)
	if es, _ := h.List(Filter{}); len(es) != 60 {
		t.Fatalf("%d entries, want 60", len(es))
	}
}

func goldenEntries() []Entry {
	t0 := time.Date(2026, 3, 14, 9, 26, 53, 0, time.UTC)
	return []Entry{
		{Time: t0, Dir: DirPull, Via: "sync", Peer: "123456789", Kind: KindText, Size: 11, Hash: "aaaaaaaaaaaabbbb", Text: "hello world"},
		{Time: t0.Add(time.Minute), Dir: DirPush, Via: "set", Peer: "123456789", Kind: KindText, Size: 38, Hash: "bbbbbbbbbbbbcccc",
			Text: "Use:\n```go\nfmt.Println(\"hi\")\n```\nand ````\n"},
		{Time: t0.Add(2 * time.Minute), Dir: DirPull, Via: "get", Peer: "123456789", Kind: KindImage, Size: 2048, Hash: "cccccccccccc",
			File: "/home/u/.config/remohop/clip/images/cccc.png"},
		{Time: t0.Add(3 * time.Minute), Dir: DirPush, Via: "push", Peer: "devbox", Kind: KindText, Size: 5000, Hash: "dd", Text: "no trailing newline\nline two", Truncated: true},
	}
}

func TestRenderMarkdownGolden(t *testing.T) {
	got := RenderMarkdown(goldenEntries())
	path := filepath.Join("testdata", "history.golden.md")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("Markdown differs from %s:\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
	if empty := RenderMarkdown(nil); !strings.Contains(empty, "0 entries") {
		t.Errorf("empty export: %q", empty)
	}
}

// Whatever the content, the fence must be longer than every backtick run in
// it, so nothing inside can close the block early.
func TestFenceCannotBeBroken(t *testing.T) {
	for _, s := range []string{"", "plain", "`", "``", "```", "a ```` b ``` c", "```\n```\n````````"} {
		fence := fenceFor(s)
		if len(fence) < 3 {
			t.Errorf("fence for %q too short", s)
		}
		for _, line := range strings.Split(s, "\n") {
			if i := strings.Index(line, fence); i >= 0 {
				t.Errorf("content %q contains the fence %q", s, fence)
			}
		}
	}
}
