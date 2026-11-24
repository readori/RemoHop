package clip

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
)

// runLocal executes `remohop clip args...` (no target).
func (c *cliRig) runLocal(t *testing.T, args ...string) int {
	t.Helper()
	c.out, c.errb = &lockedBuffer{}, &lockedBuffer{}
	env := &cli.Env{Stdin: strings.NewReader(""), Stdout: c.out, Stderr: c.errb,
		DialFunc: func(context.Context, *cli.Env) (*peer.Conn, error) {
			t.Error("a local command dialled")
			return nil, context.Canceled
		}}
	return cli.Lookup("clip").Run(context.Background(), env, args)
}

func (c *cliRig) historyJSON(t *testing.T, args ...string) []Entry {
	t.Helper()
	if code := c.run(t, "", append([]string{"history", "--json"}, args...)...); code != 0 {
		t.Fatalf("history --json %v: %d %s", args, code, c.errb)
	}
	var es []Entry
	if err := json.Unmarshal([]byte(c.out.String()), &es); err != nil {
		t.Fatalf("%v: %s", err, c.out)
	}
	return es
}

// seed records three texts for target "test": alpha (#3), beta (#2), gamma (#1).
func seed(t *testing.T, c *cliRig) {
	t.Helper()
	for _, s := range []string{"alpha", "beta", "gamma"} {
		if code := c.run(t, "", "set", s); code != 0 {
			t.Fatalf("set %s: %d %s", s, code, c.errb)
		}
	}
}

func TestCLICompetitorForms(t *testing.T) {
	c := newCLIRig(t)
	// clip set "<text>" and clip set --image are covered elsewhere; here the
	// explicit sync form and the registration shape.
	if code := c.run(t, "", "set", "quoted text with  spaces"); code != 0 || boardText(c.remote) != "quoted text with  spaces" {
		t.Fatalf("set: %d", code)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	errb := &lockedBuffer{}
	env := &cli.Env{Target: "test", Stdin: strings.NewReader(""), Stdout: &lockedBuffer{}, Stderr: errb,
		DialFunc: func(context.Context, *cli.Env) (*peer.Conn, error) { return c.client, nil }}
	go func() { done <- cli.Lookup("clip").RemoteRun(ctx, env, []string{"sync", "--push-only"}) }()
	eventually(t, "sync banner", func() bool { return strings.Contains(errb.String(), "clipboard sync with test (push-only") })
	c.local.Write(ctx, Text("via clip sync"))
	eventually(t, "push", func() bool { return boardText(c.remote) == "via clip sync" })
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("clip sync exit %d: %s", code, errb)
	}

	cmd := cli.Lookup("clip")
	if cmd.Remote || cmd.RemoteRun == nil {
		t.Fatal("clip must be a local command with a RemoteRun")
	}
	if code := c.runLocal(t, "get"); code != cli.ExitUsage || !strings.Contains(c.errb.String(), "needs a target") {
		t.Errorf("remohop clip get: %d %q", code, c.errb)
	}
	if code := c.runLocal(t); code != cli.ExitUsage {
		t.Errorf("remohop clip: %d", code)
	}
}

func TestCLIHistoryNumbersListShow(t *testing.T) {
	c := newCLIRig(t)
	seed(t, c)

	if code := c.run(t, "", "history"); code != 0 {
		t.Fatal(code, c.errb)
	}
	lines := strings.Split(strings.TrimSpace(c.out.String()), "\n")
	if len(lines) != 3 || !strings.HasPrefix(lines[0], "#1 ") || !strings.Contains(lines[0], `"gamma"`) ||
		!strings.HasPrefix(lines[2], "#3 ") || !strings.Contains(lines[2], `"alpha"`) {
		t.Fatalf("listing (newest first, numbered):\n%s", c.out)
	}
	// The same numbers whatever the cut.
	if es := c.historyJSON(t, "-n", "2"); len(es) != 2 || es[0].N != 2 || es[0].Text != "beta" || es[1].N != 1 {
		t.Fatalf("-n 2: %+v", es)
	}
	// "list" with chosen entries, flags after the numbers.
	if code := c.run(t, "", "history", "list", "3", "1", "--md"); code != 0 {
		t.Fatal(code, c.errb)
	}
	md := c.out.String()
	if !strings.Contains(md, "alpha") || !strings.Contains(md, "gamma") || strings.Contains(md, "beta") ||
		!strings.Contains(md, "(#3)") || !strings.Contains(md, "source local, sent to test") {
		t.Fatalf("list 3 1 --md:\n%s", md)
	}
	if strings.Index(md, "alpha") > strings.Index(md, "gamma") {
		t.Error("Markdown export must be oldest first")
	}
	if code := c.run(t, "", "history", "list", "9"); code != cli.ExitError || !strings.Contains(c.errb.String(), "no entry #9") {
		t.Errorf("list 9: %d %q", code, c.errb)
	}

	if code := c.run(t, "", "history", "show", "2"); code != 0 || c.out.String() != "beta" {
		t.Fatalf("show 2: %d %q %s", code, c.out, c.errb)
	}
	if !strings.Contains(c.errb.String(), "entry #2 text, 4 B, sha256 "+shortHash(Text("beta").Hash())) {
		t.Errorf("show must say which entry it acted on: %q", c.errb)
	}
	file := filepath.Join(t.TempDir(), "e.txt")
	if code := c.run(t, "", "history", "show", "3", "-o", file); code != 0 {
		t.Fatal(code, c.errb)
	}
	if b, _ := os.ReadFile(file); string(b) != "alpha" {
		t.Errorf("show -o wrote %q", b)
	}
	for _, bad := range [][]string{{"show"}, {"show", "1", "2"}, {"show", "x"}, {"show", "0"}, {"rm"}, {"copy"}, {"on", "1"}, {"clear", "1"}, {"rm", "1", "--remote"}} {
		if code := c.run(t, "", append([]string{"history"}, bad...)...); code != cli.ExitUsage {
			t.Errorf("history %v: exit %d, want usage error", bad, code)
		}
	}

	// Images: show needs -o.
	png := testPNG(t, 6, 6, false)
	in := filepath.Join(t.TempDir(), "i.png")
	os.WriteFile(in, png, 0o600)
	c.run(t, "", "set", "--image", in)
	if code := c.run(t, "", "history", "show", "1"); code != cli.ExitUsage || !strings.Contains(c.errb.String(), "-o FILE") {
		t.Errorf("show image: %d %q", code, c.errb)
	}
	out := filepath.Join(t.TempDir(), "o.png")
	if code := c.run(t, "", "history", "show", "1", "-o", out); code != 0 {
		t.Fatal(code, c.errb)
	}
	if b, _ := os.ReadFile(out); !bytes.Equal(b, png) {
		t.Error("show -o image differs")
	}
}

func TestCLIHistoryCopyRmClear(t *testing.T) {
	c := newCLIRig(t)
	seed(t, c)
	h, _ := OpenHistory("")
	h.Add(Entry{Dir: DirPull, Via: "get", Peer: "other"}, Text("elsewhere"))

	if code := c.run(t, "", "history", "copy", "3"); code != 0 || boardText(c.local) != "alpha" {
		t.Fatalf("copy 3: %d %s", code, c.errb)
	}
	if !strings.Contains(c.out.String(), "local clipboard set from entry #3 text, 5 B") {
		t.Errorf("copy output: %q", c.out)
	}
	if boardText(c.remote) != "gamma" {
		t.Error("copy must not touch the remote clipboard")
	}

	// A wrong number removes nothing, even next to a valid one.
	if code := c.run(t, "", "history", "rm", "1", "7"); code != cli.ExitError {
		t.Fatalf("rm 1 7: %d", code)
	}
	if es := c.historyJSON(t); len(es) != 3 {
		t.Fatalf("rm with a bad number removed something: %d left", len(es))
	}
	if code := c.run(t, "", "history", "rm", "2"); code != 0 || !strings.Contains(c.out.String(), "removed #2 text, 4 B, sha256 "+shortHash(Text("beta").Hash())) {
		t.Fatalf("rm 2: %d %q %s", code, c.out, c.errb)
	}
	if es := c.historyJSON(t); len(es) != 2 || es[0].Text != "alpha" || es[1].Text != "gamma" {
		t.Fatalf("after rm: %+v", es)
	}

	// clear with a target: only that target, and it says so.
	if code := c.run(t, "", "history", "clear"); code != 0 || !strings.Contains(c.out.String(), "cleared 2 history entries of test (2 text, 0 image)") {
		t.Fatalf("clear: %d %q", code, c.out)
	}
	if code := c.runLocal(t, "history", "--json"); code != 0 || !strings.Contains(c.out.String(), "elsewhere") {
		t.Fatalf("other peer's history was cleared: %s", c.out)
	}
	// Without a target the scope is every peer, listed per peer.
	c.run(t, "", "set", "again")
	if code := c.runLocal(t, "history", "clear"); code != 0 || !strings.Contains(c.out.String(), "cleared 2 history entries of all peers") ||
		!strings.Contains(c.out.String(), "  other: 1") || !strings.Contains(c.out.String(), "  test: 1") {
		t.Fatalf("local clear: %d %q", code, c.out)
	}
	if code := c.runLocal(t, "history"); code != 0 || !strings.Contains(c.out.String(), "no clipboard history") {
		t.Fatalf("after clearing all: %q", c.out)
	}
}

func TestCLIHistoryWithoutTarget(t *testing.T) {
	c := newCLIRig(t)
	seed(t, c)
	h, _ := OpenHistory("")
	h.Add(Entry{Dir: DirPull, Via: "get", Peer: "other"}, Text("elsewhere"))

	if code := c.runLocal(t, "history"); code != 0 {
		t.Fatal(code, c.errb)
	}
	if out := c.out.String(); !strings.Contains(out, "elsewhere") || !strings.Contains(out, "alpha") || !strings.HasPrefix(out, "#1 ") {
		t.Fatalf("local listing must span peers:\n%s", out)
	}
	if code := c.runLocal(t, "history", "show", "1"); code != 0 || c.out.String() != "elsewhere" {
		t.Fatalf("local show: %d %q", code, c.out)
	}
	if code := c.runLocal(t, "history", "copy", "4"); code != 0 || boardText(c.local) != "alpha" {
		t.Fatalf("local copy: %d %s", code, c.errb)
	}
	for _, sub := range []string{"send", "take", "restore"} {
		if code := c.runLocal(t, "history", sub, "1"); code != cli.ExitUsage || !strings.Contains(c.errb.String(), "needs a target") {
			t.Errorf("remohop clip history %s: %d %q", sub, code, c.errb)
		}
	}
	if code := c.runLocal(t, "history", "--remote"); code != cli.ExitUsage {
		t.Errorf("--remote without target: %d", code)
	}
}

func TestCLIHistoryOnOff(t *testing.T) {
	c := newCLIRig(t)
	if code := c.runLocal(t, "history", "off"); code != 0 || !strings.Contains(c.out.String(), "is off") {
		t.Fatalf("off: %d %q", code, c.out)
	}
	if RecordingEnabled() {
		t.Fatal("setting not persisted")
	}
	c.run(t, "", "set", "unrecorded")
	c.run(t, "", "get")
	ctx := context.Background()
	Set(ctx, c.client, Text("api too"))
	s := startSync(t, c.rig, SyncOptions{})
	c.local.Write(ctx, Text("synced while off"))
	eventually(t, "push", func() bool { return boardText(c.remote) == "synced while off" })
	s.stop(t)
	dir := filepath.Join(os.Getenv("REMOHOP_CONFIG_DIR"), "clip")
	if _, err := os.Stat(filepath.Join(dir, historyFile)); !os.IsNotExist(err) {
		t.Fatalf("history file written while recording is off: %v", err)
	}
	if es, _ := c.hist.List(Filter{}); len(es) != 0 {
		t.Fatalf("explicit store written while off: %d", len(es))
	}
	c.run(t, "", "history")
	if !strings.Contains(c.errb.String(), "recording is off") {
		t.Errorf("listing should mention that recording is off: %q", c.errb)
	}
	if code := c.run(t, "", "history", "on"); code != 0 || !RecordingEnabled() {
		t.Fatal("on failed")
	}
	c.run(t, "", "set", "recorded")
	if es := c.historyJSON(t); len(es) != 1 || es[0].Text != "recorded" {
		t.Fatalf("after on: %+v", es)
	}

	// The host-side switch is separate.
	hh, _ := OpenHostHistory()
	before, _ := hh.List(Filter{})
	if code := c.runLocal(t, "history", "off", "--host"); code != 0 || HostRecordingEnabled() || !RecordingEnabled() {
		t.Fatalf("off --host: %d", code)
	}
	c.run(t, "", "set", "host does not record this")
	after, _ := hh.List(Filter{})
	if len(after) != len(before) {
		t.Fatalf("host recorded while off: %d -> %d", len(before), len(after))
	}
	c.runLocal(t, "history", "on", "--host")
	c.run(t, "", "set", "host records this")
	if after, _ = hh.List(Filter{}); len(after) != len(before)+1 {
		t.Fatalf("host did not resume recording")
	}
}

func TestCLIHistorySend(t *testing.T) {
	c := newCLIRig(t)
	h, _ := OpenHistory("")
	h.Add(Entry{Dir: DirPull, Via: "get", Peer: "other", Time: time.Now().Add(-time.Hour)}, Text("from the other box"))
	seed(t, c)

	// In the target's scope entry 3 is "alpha".
	if code := c.run(t, "", "history", "send", "3"); code != 0 || boardText(c.remote) != "alpha" {
		t.Fatalf("send 3: %d %s", code, c.errb)
	}
	if !strings.Contains(c.out.String(), "remote clipboard set from entry #3 text, 5 B") {
		t.Errorf("send output: %q", c.out)
	}
	// With --all the numbering is the all-peers listing: the other peer's
	// entry is the oldest.
	if code := c.runLocal(t, "history", "--json"); code != 0 {
		t.Fatal(code)
	}
	var all []Entry
	json.Unmarshal([]byte(c.out.String()), &all)
	if all[0].Text != "from the other box" {
		t.Fatalf("oldest entry: %+v", all[0])
	}
	if code := c.run(t, "", "history", "send", "--all", itoa(all[0].N)); code != 0 || boardText(c.remote) != "from the other box" {
		t.Fatalf("send --all: %d %s", code, c.errb)
	}
	c.runLocal(t, "history", "--json", "-n", "0")
	all = nil
	json.Unmarshal([]byte(c.out.String()), &all)
	orig, last := all[0], all[len(all)-1]
	if orig.Source != "other" || len(orig.SentTo) != 1 || orig.SentTo[0] != "test" {
		t.Errorf("original entry should note it was sent to test: %+v", orig)
	}
	if last.Via != "send" || last.Peer != "test" || last.Source != "other" || len(last.SentTo) != 1 || last.SentTo[0] != "test" {
		t.Errorf("send record: %+v", last)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestSourceAndSentToRecorded(t *testing.T) {
	c := newCLIRig(t)
	c.run(t, "", "set", "out")
	c.remote.Write(context.Background(), Text("in"))
	c.run(t, "", "get")
	es := c.historyJSON(t)
	if len(es) != 2 || es[0].Source != SourceLocal || len(es[0].SentTo) != 1 || es[0].SentTo[0] != "test" ||
		es[1].Source != "test" || len(es[1].SentTo) != 0 || es[0].ID == "" || es[0].ID == es[1].ID {
		t.Fatalf("entries: %+v", es)
	}
}

func TestHostHistoryTakeRestoreRemote(t *testing.T) {
	c := newCLIRig(t)
	ctx := context.Background()
	c.run(t, "", "set", "first on host")
	long := strings.Repeat("L", 1000)
	c.remote.Write(ctx, Text(long))
	c.run(t, "", "get")
	img, _ := PNG(testPNG(t, 8, 8, false))
	if err := Set(ctx, c.client, img); err != nil {
		t.Fatal(err)
	}
	s := startSync(t, c.rig, SyncOptions{})
	c.remote.Write(ctx, Text("copied on the host during sync"))
	eventually(t, "pull", func() bool { return boardText(c.local) == "copied on the host during sync" })
	s.stop(t)

	// The host kept its own record, privately.
	hh, err := OpenHostHistory()
	if err != nil {
		t.Fatal(err)
	}
	hes, _ := hh.List(Filter{})
	if len(hes) != 4 {
		t.Fatalf("host history has %d entries: %+v", len(hes), hes)
	}
	// Seen from the host: what the client set came from the client; what the
	// client fetched is the host's own, sent to the client.
	if hes[0].Dir != DirPush || hes[0].Source != "test-client" || hes[1].Dir != DirPull || hes[1].Source != SourceLocal ||
		len(hes[1].SentTo) != 1 || hes[1].SentTo[0] != "test-client" || hes[3].Via != "sync" {
		t.Fatalf("host entries: %+v", hes)
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{hh.Dir(), filepath.Join(hh.Dir(), imageDir)} {
			if st, _ := os.Stat(p); st.Mode().Perm() != 0o700 {
				t.Errorf("%s mode %v, want 0700", p, st.Mode().Perm())
			}
		}
		for _, p := range []string{filepath.Join(hh.Dir(), historyFile), hes[2].File} {
			if st, err := os.Stat(p); err != nil || st.Mode().Perm() != 0o600 {
				t.Errorf("%s mode, want 0600 (%v)", p, err)
			}
		}
	}

	// Over the wire: bounded, previews only, no host paths.
	res, err := RemoteHistory(ctx, c.client, 0)
	if err != nil || len(res) != 4 || res[3].N != 1 || res[0].N != 4 {
		t.Fatalf("RemoteHistory: %v %+v", err, res)
	}
	if len(res[1].Text) > previewBytes || !res[1].Truncated || res[1].Size != 1000 || res[2].File != "" || res[2].Kind != KindImage {
		t.Fatalf("listing must carry previews only: %+v", res[1:3])
	}
	if two, _ := RemoteHistory(ctx, c.client, 2); len(two) != 2 || two[1].N != 1 {
		t.Fatalf("limit 2: %+v", two)
	}
	if _, err := RemoteHistoryGet(ctx, c.client, "nope"); err == nil {
		t.Error("unknown id accepted")
	}
	if _, err := RemoteHistoryGet(ctx, c.client, ""); err == nil {
		t.Error("empty id accepted")
	}
	full, err := RemoteHistoryGet(ctx, c.client, res[1].ID)
	if err != nil || full.String() != long {
		t.Fatalf("RemoteHistoryGet: %v", err)
	}

	// CLI: list, show, take, restore.
	if code := c.run(t, "", "history", "--remote"); code != 0 {
		t.Fatal(code, c.errb)
	}
	lines := strings.Split(strings.TrimSpace(c.out.String()), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "#1 ") || !strings.Contains(lines[0], "copied on the host") || !strings.Contains(lines[1], "(image)") {
		t.Fatalf("remote listing:\n%s", c.out)
	}
	if code := c.run(t, "", "history", "show", "--remote", "3"); code != 0 || c.out.String() != long {
		t.Fatalf("show --remote 3: %d %s", code, c.errb)
	}
	if code := c.run(t, "", "history", "take", "4"); code != 0 || boardText(c.local) != "first on host" {
		t.Fatalf("take 4: %d %s", code, c.errb)
	}
	if !strings.Contains(c.out.String(), "local clipboard set from remote entry #4 text, 13 B") {
		t.Errorf("take output: %q", c.out)
	}
	if es := c.historyJSON(t, "-n", "1"); len(es) != 1 || es[0].Via != "take" || es[0].Dir != DirPull || es[0].Source != "test" {
		t.Errorf("take not recorded locally: %+v", es)
	}
	if code := c.run(t, "", "history", "take", "2"); code != 0 || boardHash(c.local) != img.Hash() {
		t.Fatalf("take image: %d %s", code, c.errb)
	}
	if code := c.run(t, "", "history", "restore", "4"); code != 0 || boardText(c.remote) != "first on host" {
		t.Fatalf("restore 4: %d %s", code, c.errb)
	}
	if !strings.Contains(c.out.String(), "remote clipboard set from remote entry #4 text") {
		t.Errorf("restore output: %q", c.out)
	}
	if code := c.run(t, "", "history", "restore", "50"); code != cli.ExitError || !strings.Contains(c.errb.String(), "no entry #50") {
		t.Errorf("restore 50: %d %q", code, c.errb)
	}
	if boardText(c.remote) != "first on host" {
		t.Error("a failed restore changed the remote clipboard")
	}
}

func TestRemoteHistoryListIsBounded(t *testing.T) {
	r := newRig(t)
	hh, _ := OpenHostHistory()
	for i := 0; i < MaxRemoteList+20; i++ {
		hh.Add(Entry{Dir: DirPush, Via: "set", Peer: "p", Time: time.Unix(int64(1000+i), 0)}, Text(strings.Repeat("x", i+1)))
	}
	res, err := RemoteHistory(context.Background(), r.client, 100000)
	if err != nil || len(res) != MaxRemoteList || res[len(res)-1].Size != MaxRemoteList+20 {
		t.Fatalf("RemoteHistory: %d entries, %v", len(res), err)
	}
}

// A history file written before entries had ids, sources and sent-to lists
// keeps working and is upgraded in place by the next change.
func TestHistoryMigratesOldFormat(t *testing.T) {
	c := newCLIRig(t)
	dir := filepath.Join(os.Getenv("REMOHOP_CONFIG_DIR"), "clip")
	os.MkdirAll(filepath.Join(dir, imageDir), 0o700)
	old := `{"time":"2026-01-02T03:04:05Z","dir":"push","via":"set","peer":"test","kind":"text","size":3,"hash":"` + Text("old").Hash() + `","text":"old"}
{"time":"2026-01-02T03:05:05Z","dir":"pull","via":"sync","peer":"test","kind":"text","size":5,"hash":"` + Text("older").Hash() + `","text":"older"}
`
	if err := os.WriteFile(filepath.Join(dir, historyFile), []byte(old), 0o600); err != nil {
		t.Fatal(err)
	}
	es := c.historyJSON(t)
	if len(es) != 2 || es[0].ID == "" || es[0].Source != SourceLocal || len(es[0].SentTo) != 1 || es[0].SentTo[0] != "test" ||
		es[1].Source != "test" || es[1].N != 1 || es[0].N != 2 {
		t.Fatalf("migrated view: %+v", es)
	}
	// Ids are stable across reads, so numbers resolved now still hit.
	if again := c.historyJSON(t); again[0].ID != es[0].ID {
		t.Fatal("ids of old entries are not stable")
	}
	if code := c.run(t, "", "history", "show", "2"); code != 0 || c.out.String() != "old" {
		t.Fatalf("show on an old entry: %d %s", code, c.errb)
	}
	if code := c.run(t, "", "history", "rm", "1"); code != 0 {
		t.Fatal(code, c.errb)
	}
	b, _ := os.ReadFile(filepath.Join(dir, historyFile))
	if !strings.Contains(string(b), `"source":"local"`) || !strings.Contains(string(b), `"sent_to":["test"]`) || strings.Contains(string(b), "older") {
		t.Fatalf("file not rewritten in the new format:\n%s", b)
	}
}

func TestMarkdownCap(t *testing.T) {
	var es []Entry
	for i := 0; i < 10; i++ {
		es = append(es, Entry{Time: time.Unix(int64(i), 0), Dir: DirPull, Via: "get", Peer: "p", Kind: KindText,
			Size: 1000, Hash: "h", Text: strings.Repeat(string(rune('a'+i)), 1000), N: 10 - i})
	}
	full, shown := RenderMarkdownLimit(es, 0)
	if shown != 10 || strings.Contains(full, "truncated") {
		t.Fatal("uncapped export was cut")
	}
	md, shown := RenderMarkdownLimit(es, 4000)
	if len(md) > 4000 || shown == 0 || shown >= 10 {
		t.Fatalf("cap 4000: %d bytes, %d shown", len(md), shown)
	}
	if !strings.Contains(md, "jjjj") || strings.Contains(md, "aaaa") || !strings.Contains(md, "output truncated at 4000 bytes") {
		t.Fatalf("the newest entries must survive and the cut be stated:\n%.300s", md)
	}
	// Smaller than one entry: the newest entry's text is cut, not dropped.
	md, shown = RenderMarkdownLimit(es, 900)
	if len(md) > 900 || shown != 1 || !strings.Contains(md, "jjjj") || !strings.Contains(md, "(truncated: the first") {
		t.Fatalf("cap 900: %d bytes, %d shown:\n%s", len(md), shown, md)
	}

	c := newCLIRig(t)
	for i := 0; i < 5; i++ {
		c.run(t, "", "set", strings.Repeat(string(rune('a'+i)), 800))
	}
	if code := c.run(t, "", "history", "--md", "--max-bytes", "2500"); code != 0 {
		t.Fatal(code, c.errb)
	}
	if len(c.out.String()) > 2500 || !strings.Contains(c.out.String(), "output truncated") || !strings.Contains(c.errb.String(), "truncated at 2500 bytes") {
		t.Fatalf("--max-bytes: %d bytes, stderr %q", len(c.out.String()), c.errb)
	}
	if code := c.run(t, "", "history", "--md", "--max-bytes", "0"); code != 0 || strings.Contains(c.out.String(), "output truncated") {
		t.Fatal("--max-bytes 0 must not cap")
	}
}

func TestSplitArgs(t *testing.T) {
	flags, pos := splitArgs([]string{"list", "3", "-n", "5", "1", "--md", "-o", "-", "--since=2h", "--", "-x"}, historyValueFlags)
	if strings.Join(flags, " ") != "-n 5 --md -o - --since=2h" || strings.Join(pos, " ") != "list 3 1 -x" {
		t.Fatalf("flags=%q pos=%q", flags, pos)
	}
}

// The host records before an item takes effect where the client can see it
// (the reply to get/set, the item sent or written during sync). So when the
// client has observed a transfer, the host's record of it already exists:
// no recording runs after the session, which on Windows broke temp-dir
// cleanup. Checked without any waiting.
func TestHostRecordsBeforeTheClientSeesTheItem(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	hh, _ := OpenHostHistory()
	count := func() int { es, _ := hh.List(Filter{}); return len(es) }

	if err := Set(ctx, r.client, Text("set")); err != nil || count() != 1 {
		t.Fatalf("after Set: %d host entries, %v", count(), err)
	}
	if _, err := Get(ctx, r.client); err != nil || count() != 2 {
		t.Fatalf("after Get: %d host entries, %v", count(), err)
	}
	s := startSync(t, r, SyncOptions{NoHistory: true})
	r.local.Write(ctx, Text("pushed"))
	eventually(t, "push", func() bool { return boardText(r.remote) == "pushed" })
	if n := count(); n != 3 {
		t.Fatalf("host board updated before its record: %d entries", n)
	}
	r.remote.Write(ctx, Text("pulled"))
	eventually(t, "pull", func() bool { return boardText(r.local) == "pulled" })
	if n := count(); n != 4 {
		t.Fatalf("client received an item before the host recorded it: %d entries", n)
	}
	s.stop(t)
}
