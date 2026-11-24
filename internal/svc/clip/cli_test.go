package clip

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
)

// lockedBuffer lets a test read what a running command has logged.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

type cliRig struct {
	*rig
	out, errb *lockedBuffer
}

func newCLIRig(t *testing.T) *cliRig {
	t.Helper()
	r := newRig(t)
	old := localBoard
	localBoard = func(context.Context) Board { return r.local }
	t.Cleanup(func() { localBoard = old })
	return &cliRig{rig: r, out: &lockedBuffer{}, errb: &lockedBuffer{}}
}

// run executes `remohop test clip args...`.
func (c *cliRig) run(t *testing.T, stdin string, args ...string) int {
	t.Helper()
	return c.runCtx(t, context.Background(), stdin, args...)
}

func (c *cliRig) runCtx(t *testing.T, ctx context.Context, stdin string, args ...string) int {
	t.Helper()
	c.out, c.errb = &lockedBuffer{}, &lockedBuffer{}
	env := &cli.Env{
		Target: "test",
		Stdin:  strings.NewReader(stdin),
		Stdout: c.out,
		Stderr: c.errb,
		DialFunc: func(context.Context, *cli.Env) (*peer.Conn, error) {
			return c.client, nil
		},
	}
	return cli.Lookup("clip").RemoteRun(ctx, env, args)
}

func TestCLISetGet(t *testing.T) {
	c := newCLIRig(t)

	if code := c.run(t, "", "set", "hello", "big", "world"); code != 0 {
		t.Fatalf("set: %d %s", code, c.errb)
	}
	if !strings.Contains(c.out.String(), "backend: memory") {
		t.Errorf("set output lacks the backend: %q", c.out)
	}
	if got := boardText(c.remote); got != "hello big world" {
		t.Fatalf("remote = %q", got)
	}

	if code := c.run(t, "from stdin\nline2\n", "set"); code != 0 {
		t.Fatalf("set stdin: %d %s", code, c.errb)
	}
	if got := boardText(c.remote); got != "from stdin\nline2\n" {
		t.Fatalf("remote = %q", got)
	}

	if code := c.run(t, "", "get"); code != 0 || c.out.String() != "from stdin\nline2\n" {
		t.Fatalf("get: %d %q %s", code, c.out, c.errb)
	}

	file := filepath.Join(t.TempDir(), "out.txt")
	if code := c.run(t, "", "get", "-o", file); code != 0 {
		t.Fatalf("get -o: %d %s", code, c.errb)
	}
	if b, _ := os.ReadFile(file); string(b) != "from stdin\nline2\n" {
		t.Fatalf("file = %q", b)
	}
	if runtime.GOOS != "windows" {
		if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
			t.Errorf("-o file mode %v", st.Mode().Perm())
		}
	}

	if code := c.run(t, "", "get", "--json"); code != 0 {
		t.Fatalf("get --json: %d", code)
	}
	var item itemJSON
	if err := json.Unmarshal([]byte(c.out.String()), &item); err != nil || item.Kind != KindText ||
		item.Text != "from stdin\nline2\n" || item.Backend != "memory" || item.Size != 17 || item.Hash == "" {
		t.Fatalf("json = %+v, %v", item, err)
	}

	if code := c.run(t, "", "set", "--json", "x"); code != 0 || !strings.Contains(c.out.String(), `"ok": true`) {
		t.Fatalf("set --json: %d %q", code, c.out)
	}
}

func TestCLIEmptyAndUsageErrors(t *testing.T) {
	c := newCLIRig(t)
	if code := c.run(t, "", "get"); code != cli.ExitError || !strings.Contains(c.errb.String(), "empty") {
		t.Errorf("get on empty: %d %q", code, c.errb)
	}
	if code := c.run(t, "", "get", "--json"); code != 0 || !strings.Contains(c.out.String(), `"empty": true`) {
		t.Errorf("get --json on empty: %d %q", code, c.out)
	}
	if code := c.run(t, "", "push"); code != cli.ExitError || !strings.Contains(c.errb.String(), "local clipboard is empty") {
		t.Errorf("push empty: %d %q", code, c.errb)
	}
	if code := c.run(t, "", "pull"); code != cli.ExitError {
		t.Errorf("pull empty: %d", code)
	}
	if code := c.run(t, "", "--bogus"); code != cli.ExitUsage {
		t.Errorf("bad flag: %d", code)
	}
	if code := c.run(t, "", "--push-only", "--pull-only"); code != cli.ExitUsage {
		t.Errorf("exclusive flags: %d", code)
	}
	if code := c.run(t, "", "set", "--image", "x.png", "text"); code != cli.ExitUsage {
		t.Errorf("image+text: %d", code)
	}
	if code := c.run(t, "not an image", "set", "--image", "-"); code != cli.ExitError {
		t.Errorf("garbage image: %d", code)
	}
	if code := c.run(t, "", "history", "--md", "--json"); code != cli.ExitUsage {
		t.Errorf("md+json: %d", code)
	}
	if code := c.run(t, "", "history", "--since", "soon"); code != cli.ExitUsage {
		t.Errorf("bad since: %d", code)
	}
	if code := c.run(t, "", "-h"); code != 0 || !strings.Contains(c.errb.String(), "Usage: remohop <ID> clip") {
		t.Errorf("-h: %d", code)
	}
}

func TestCLIImages(t *testing.T) {
	c := newCLIRig(t)
	pngBytes := testPNG(t, 20, 20, false)
	dir := t.TempDir()
	in := filepath.Join(dir, "in.png")
	os.WriteFile(in, pngBytes, 0o600)

	if code := c.run(t, "", "set", "--image", in); code != 0 {
		t.Fatalf("set --image: %d %s", code, c.errb)
	}
	if rc, err := c.remote.Read(context.Background()); err != nil || rc.Kind != KindImage {
		t.Fatalf("remote: %+v %v", rc, err)
	}

	// An image without -o explains instead of spraying binary on stdout.
	if code := c.run(t, "", "get"); code != cli.ExitUsage || !strings.Contains(c.errb.String(), "-o FILE") || c.out.String() != "" {
		t.Errorf("get image without -o: %d %q %q", code, c.errb, c.out)
	}
	out := filepath.Join(dir, "out.png")
	if code := c.run(t, "", "get", "-o", out); code != 0 {
		t.Fatalf("get -o: %d %s", code, c.errb)
	}
	if b, _ := os.ReadFile(out); !bytes.Equal(b, pngBytes) {
		t.Error("saved image differs")
	}
	if code := c.run(t, "", "get", "-o", "-"); code != 0 || !bytes.Equal([]byte(c.out.String()), pngBytes) {
		t.Errorf("get -o -: %d", code)
	}
	if code := c.run(t, "", "get", "--json"); code != 0 {
		t.Fatal(code)
	}
	var item itemJSON
	if err := json.Unmarshal([]byte(c.out.String()), &item); err != nil || item.Kind != KindImage || item.MIME != MIMEImage || item.Data == "" {
		t.Fatalf("json: %+v %v", item, err)
	}
	if code := c.run(t, "", "get", "--json", "-o", out); code != 0 {
		t.Fatal(code)
	}
	item = itemJSON{}
	json.Unmarshal([]byte(c.out.String()), &item)
	if item.File != out || item.Data != "" {
		t.Errorf("json with -o: %+v", item)
	}
	if code := c.run(t, string(pngBytes), "set", "--image", "-"); code != 0 {
		t.Errorf("set --image - : %d %s", code, c.errb)
	}
}

func TestCLIPushPull(t *testing.T) {
	c := newCLIRig(t)
	ctx := context.Background()
	c.local.Write(ctx, Text("local text"))
	if code := c.run(t, "", "push"); code != 0 || boardText(c.remote) != "local text" {
		t.Fatalf("push: %d %s", code, c.errb)
	}
	c.remote.Write(ctx, Text("remote text"))
	if code := c.run(t, "", "pull", "--json"); code != 0 || boardText(c.local) != "remote text" ||
		!strings.Contains(c.out.String(), `"local_backend": "memory"`) {
		t.Fatalf("pull: %d %s %s", code, c.out, c.errb)
	}
	img, _ := PNG(testPNG(t, 9, 9, false))
	c.remote.Write(ctx, img)
	if code := c.run(t, "", "pull"); code != 0 || boardHash(c.local) != img.Hash() {
		t.Fatalf("pull image: %d", code)
	}

	// push/pull are recorded with their own operation names.
	if code := c.run(t, "", "history", "--json"); code != 0 {
		t.Fatal(code)
	}
	var es []Entry
	if err := json.Unmarshal([]byte(c.out.String()), &es); err != nil || len(es) != 3 {
		t.Fatalf("history json: %v %s", err, c.out)
	}
	if es[0].Via != "push" || es[1].Via != "pull" || es[2].Kind != KindImage || es[2].File == "" {
		t.Errorf("entries: %+v", es)
	}
}

func TestCLIStatus(t *testing.T) {
	c := newCLIRig(t)
	if code := c.run(t, "", "status", "--json"); code != 0 {
		t.Fatal(code)
	}
	var st map[string]any
	json.Unmarshal([]byte(c.out.String()), &st)
	if st["local_backend"] != "memory" || st["remote_backend"] != "memory" {
		t.Errorf("status: %v", st)
	}
	if code := c.run(t, "", "status"); code != 0 || !strings.Contains(c.out.String(), "remote clipboard: memory") {
		t.Errorf("status text: %q", c.out)
	}
}

func TestCLIHistory(t *testing.T) {
	c := newCLIRig(t)
	c.run(t, "", "set", "first ``` fence")
	c.run(t, "", "set", "second")
	c.run(t, "", "get")

	if code := c.run(t, "", "history"); code != 0 {
		t.Fatal(code)
	}
	for _, want := range []string{"push", "pull", `"first ` + "``` " + `fence"`, `"second"`} {
		if !strings.Contains(c.out.String(), want) {
			t.Errorf("history output lacks %q:\n%s", want, c.out)
		}
	}
	if code := c.run(t, "", "history", "-n", "1", "--json"); code != 0 {
		t.Fatal(code)
	}
	var es []Entry
	json.Unmarshal([]byte(c.out.String()), &es)
	if len(es) != 1 || es[0].Dir != DirPull || es[0].Text != "second" {
		t.Errorf("-n 1: %+v", es)
	}
	if code := c.run(t, "", "history", "--md"); code != 0 {
		t.Fatal(code)
	}
	md := c.out.String()
	if !strings.Contains(md, "````text\nfirst ``` fence\n````") || !strings.Contains(md, "## ") || !strings.Contains(md, "push to test") {
		t.Errorf("markdown:\n%s", md)
	}
	if code := c.run(t, "", "history", "--since", "1h", "--json"); code != 0 || !strings.Contains(c.out.String(), `"second"`) {
		t.Errorf("since 1h: %d %s", code, c.out)
	}
	if code := c.run(t, "", "history", "--since", "1d", "--json"); code != 0 {
		t.Errorf("since 1d: %d", code)
	}

	// Per-target by default; --all spans peers.
	h, _ := OpenHistory("")
	h.Add(Entry{Dir: DirPull, Via: "get", Peer: "other"}, Text("elsewhere"))
	c.run(t, "", "history", "--json")
	if strings.Contains(c.out.String(), "elsewhere") {
		t.Error("history leaked another peer's entries")
	}
	c.run(t, "", "history", "--all", "--json")
	if !strings.Contains(c.out.String(), "elsewhere") {
		t.Error("--all missed another peer")
	}

	if code := c.run(t, "", "history", "--clear"); code != 0 || !strings.Contains(c.out.String(), "cleared 3 history entries") {
		t.Fatalf("clear: %d %q", code, c.out)
	}
	c.run(t, "", "history", "--all", "--json")
	if !strings.Contains(c.out.String(), "elsewhere") || strings.Contains(c.out.String(), "second") {
		t.Errorf("--clear should only drop this target:\n%s", c.out)
	}
	c.run(t, "", "history")
	if !strings.Contains(c.out.String(), "no clipboard history") {
		t.Errorf("empty history: %q", c.out)
	}
}

func TestCLISyncForeground(t *testing.T) {
	c := newCLIRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 1)
	out, errb := &lockedBuffer{}, &lockedBuffer{}
	env := &cli.Env{Target: "test", Stdin: strings.NewReader(""), Stdout: out, Stderr: errb,
		DialFunc: func(context.Context, *cli.Env) (*peer.Conn, error) { return c.client, nil }}
	go func() { done <- cli.Lookup("clip").RemoteRun(ctx, env, []string{"--no-images"}) }()

	eventually(t, "sync banner", func() bool { return strings.Contains(errb.String(), "clipboard sync with test") })
	c.local.Write(ctx, Text("synced"))
	eventually(t, "push", func() bool { return boardText(c.remote) == "synced" })
	c.remote.Write(ctx, Text("back"))
	eventually(t, "pull", func() bool { return boardText(c.local) == "back" })
	eventually(t, "log lines", func() bool {
		s := errb.String()
		return strings.Contains(s, "clip: pushed text (6 B)") && strings.Contains(s, "clip: pulled text (4 B)")
	})
	if !strings.Contains(errb.String(), "text only") || !strings.Contains(errb.String(), "local: memory") {
		t.Errorf("banner: %q", errb)
	}
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errb)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("clip did not stop")
	}
}
