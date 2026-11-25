package mcp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/svc/clip"
	"github.com/readori/RemoHop/internal/svc/wake"
	"github.com/readori/RemoHop/internal/wire"
)

func httpGet(t *testing.T, url string) (string, error) {
	t.Helper()
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func TestForward(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "hello from the remote side") }))
	t.Cleanup(web.Close)
	_, port, _ := net.SplitHostPort(web.Listener.Addr().String())

	h := newHarness(t)
	var fi forwardInfo
	res := h.ok("remohop_forward_start", map[string]any{"spec": "0:" + port}, &fi)
	if !strings.HasPrefix(fi.Listen, "127.0.0.1:") || fi.ID == "" || fi.Reverse || !strings.Contains(text(res), fi.Listen) {
		t.Fatalf("start: %+v / %s", fi, text(res))
	}
	body, err := httpGet(t, "http://"+fi.Listen+"/")
	if err != nil || body != "hello from the remote side" {
		t.Fatalf("GET through forward: %q %v", body, err)
	}

	// The counters are updated by the copying goroutines, which may still be
	// finishing when the HTTP client already has its response.
	var list forwardListOut
	eventually(t, "forward statistics", func() bool {
		h.ok("remohop_forward_list", nil, &list)
		return len(list.Forwards) == 1 && list.Forwards[0].Connections == 1 && list.Forwards[0].BytesDown > 0 && list.Forwards[0].BytesUp > 0
	})
	h.ok("remohop_forward_list", map[string]any{"target": "555555555"}, &list)
	if len(list.Forwards) != 0 {
		t.Fatalf("target filter: %+v", list)
	}

	h.ok("remohop_forward_stop", map[string]any{"id": fi.ID}, nil)
	if _, err := httpGet(t, "http://"+fi.Listen+"/"); err == nil {
		t.Fatal("forward still answers after stop")
	}
	eventually(t, "forward removal", func() bool {
		h.ok("remohop_forward_list", nil, &list)
		return len(list.Forwards) == 0
	})
	if msg := h.fail("remohop_forward_stop", map[string]any{"id": fi.ID}); !strings.Contains(msg, "no active forward") {
		t.Errorf("stop unknown: %s", msg)
	}
	if msg := h.fail("remohop_forward_start", map[string]any{"spec": "nonsense"}); !strings.Contains(msg, "bad spec") {
		t.Errorf("bad spec: %s", msg)
	}
}

func TestForwardKeepsSessionAliveAndEndsAtShutdown(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") }))
	t.Cleanup(web.Close)
	_, port, _ := net.SplitHostPort(web.Listener.Addr().String())

	h := newHarness(t, func(o *Options) { o.IdleTimeout = 100 * time.Millisecond })
	var fi forwardInfo
	h.ok("remohop_forward_start", map[string]any{"spec": "0:" + port}, &fi)
	time.Sleep(500 * time.Millisecond) // several idle periods
	if body, err := httpGet(t, "http://"+fi.Listen+"/"); err != nil || body != "ok" {
		t.Fatalf("forward died while idle: %q %v", body, err)
	}
	h.srv.Close()
	if _, err := httpGet(t, "http://"+fi.Listen+"/"); err == nil {
		t.Fatal("forward survived shutdown")
	}
}

func TestReverseForward(t *testing.T) {
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "from the local side") }))
	t.Cleanup(web.Close)
	_, port, _ := net.SplitHostPort(web.Listener.Addr().String())

	h := newHarness(t)
	var fi forwardInfo
	h.ok("remohop_forward_start", map[string]any{"spec": "0:" + port, "reverse": true}, &fi)
	if !fi.Reverse {
		t.Fatalf("not reverse: %+v", fi)
	}
	// Host and client share this machine, so the "remote" listener is reachable.
	addr := strings.Replace(fi.Listen, "[::]", "127.0.0.1", 1)
	body, err := httpGet(t, "http://"+addr+"/")
	if err != nil || body != "from the local side" {
		t.Fatalf("GET via reverse forward (%s): %q %v", fi.Listen, body, err)
	}
}

func TestClipboard(t *testing.T) {
	board := clip.NewMemoryBoard()
	clip.SetHostBoard(board)
	t.Cleanup(func() { clip.SetHostBoard(clip.Virtual()) })

	h := newHarness(t)
	var got clipGetOut
	h.ok("remohop_clipboard_get", nil, &got)
	if got.Kind != "empty" {
		t.Fatalf("empty clipboard: %+v", got)
	}

	var set clipSetOut
	h.ok("remohop_clipboard_set", map[string]any{"text": "héllo\nclipboard"}, &set)
	if set.Bytes != len("héllo\nclipboard") {
		t.Fatalf("set: %+v", set)
	}
	res := h.ok("remohop_clipboard_get", nil, &got)
	if got.Kind != "text" || got.Text != "héllo\nclipboard" || text(res) != "héllo\nclipboard" {
		t.Fatalf("get: %+v / %q", got, text(res))
	}
	h.ok("remohop_clipboard_get", map[string]any{"max_bytes": 3}, &got)
	if !got.Truncated || got.Text != "hé" {
		t.Fatalf("truncated get: %+v", got)
	}
	save := filepath.Join(t.TempDir(), "clip.txt")
	h.ok("remohop_clipboard_get", map[string]any{"save_path": save}, &got)
	if b, _ := os.ReadFile(save); string(b) != "héllo\nclipboard" {
		t.Fatalf("saved clipboard: %q", b)
	}
	if msg := h.fail("remohop_clipboard_get", map[string]any{"save_path": "../relative.txt"}); !strings.Contains(msg, "must stay inside") {
		t.Errorf("relative save path out of the download dir: %s", msg)
	}
	if msg := h.fail("remohop_clipboard_set", map[string]any{"text": ""}); !strings.Contains(msg, "empty") {
		t.Errorf("empty set: %s", msg)
	}

	// An image on the remote clipboard comes back as an image block.
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		t.Fatal(err)
	}
	c, err := clip.PNG(pngBuf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if err := board.Write(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	res = h.ok("remohop_clipboard_get", nil, &got)
	var image *sdk.ImageContent
	for _, c := range res.Content {
		if ic, ok := c.(*sdk.ImageContent); ok {
			image = ic
		}
	}
	if got.Kind != "image" || image == nil || image.MIMEType != "image/png" || !bytes.Equal(image.Data, pngBuf.Bytes()) {
		t.Fatalf("image get: %+v %v", got, image)
	}

	var hist clipHistoryOut
	res = h.ok("remohop_clipboard_history", nil, &hist)
	if hist.Entries < 3 || !strings.Contains(text(res), "héllo") || !strings.Contains(text(res), "clipboard") {
		t.Fatalf("history (%d entries): %s", hist.Entries, text(res))
	}
	h.ok("remohop_clipboard_history", map[string]any{"limit": 1, "since": "1h"}, &hist)
	if hist.Entries != 1 {
		t.Fatalf("limit: %+v", hist)
	}
	h.ok("remohop_clipboard_history", map[string]any{"peer": "nobody"}, &hist)
	if hist.Entries != 0 {
		t.Fatalf("peer filter: %+v", hist)
	}
	if msg := h.fail("remohop_clipboard_history", map[string]any{"since": "yesterday-ish"}); !strings.Contains(msg, "since") {
		t.Errorf("bad since: %s", msg)
	}
}

func TestScreenshot(t *testing.T) {
	display := startXvfb(t)
	t.Setenv("DISPLAY", fmt.Sprintf(":%d", display))
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("XAUTHORITY", "")

	h := newHarness(t)
	var dl displaysOut
	h.ok("remohop_displays", nil, &dl)
	if len(dl.Displays) == 0 || dl.Displays[0].Width != 640 || dl.Displays[0].Height != 480 {
		t.Fatalf("displays: %+v", dl)
	}

	save := filepath.Join(t.TempDir(), "screen.png")
	var out screenshotOut
	res := h.ok("remohop_screenshot", map[string]any{"max_width": 320, "save_path": save}, &out)
	var ic *sdk.ImageContent
	for _, c := range res.Content {
		if v, ok := c.(*sdk.ImageContent); ok {
			ic = v
		}
	}
	if ic == nil || ic.MIMEType != "image/png" {
		t.Fatalf("no png image block: %+v", res.Content)
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(ic.Data))
	if err != nil || cfg.Width != 320 || cfg.Height != 240 || out.Width != 320 || out.Height != 240 || out.Format != "png" {
		t.Fatalf("image %v %+v / out %+v", err, cfg, out)
	}
	if b, err := os.ReadFile(save); err != nil || !bytes.Equal(b, ic.Data) {
		t.Fatalf("saved file differs (%v)", err)
	}
	// The wire form is base64 inside the content block.
	raw, _ := ic.MarshalJSON()
	if !strings.Contains(string(raw), base64.StdEncoding.EncodeToString(ic.Data[:12])) {
		t.Errorf("image block is not base64 data")
	}

	res = h.ok("remohop_screenshot", map[string]any{"format": "jpeg"}, &out)
	if out.MIME != "image/jpeg" || out.Width != 640 {
		t.Fatalf("jpeg: %+v", out)
	}
	if msg := h.fail("remohop_screenshot", map[string]any{"format": "bmp"}); !strings.Contains(msg, "png or jpeg") {
		t.Errorf("bad format: %s", msg)
	}
	if msg := h.fail("remohop_screenshot", map[string]any{"display": 9}); msg == "" {
		t.Error("bad display accepted")
	}
}

func TestScreenshotWithoutDisplay(t *testing.T) {
	// A host that answers the way a headless one does. The real screenshot
	// service would find any X server running on the machine, so this test
	// must not depend on it.
	reg := peer.NewRegistry()
	reg.Handle("screenshot", func(_ context.Context, _ *peer.Conn, s peer.Stream, _ json.RawMessage) error {
		return wire.WriteJSON(s, map[string]string{"error": "no display available on the host: DISPLAY is not set"})
	})
	h := newHarnessWith(t, reg)
	for _, tool := range []string{"remohop_screenshot", "remohop_displays"} {
		if msg := h.fail(tool, nil); !strings.Contains(msg, "headless") || !strings.Contains(msg, "no display") {
			t.Errorf("%s: headless hint missing: %s", tool, msg)
		}
	}
}

// startXvfb runs Xvfb on a free display and returns its number.
func startXvfb(t *testing.T) int {
	t.Helper()
	bin, err := exec.LookPath("Xvfb")
	if err != nil {
		t.Skip("Xvfb is not installed")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "-displayfd", "3", "-screen", "0", "640x480x24", "-nolisten", "tcp", "-noreset")
	cmd.ExtraFiles = []*os.File{w}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	exited := make(chan struct{})
	go func() { cmd.Wait(); close(exited) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-exited
		r.Close()
	})
	line := make(chan string, 1)
	go func() {
		var b [16]byte
		n, _ := r.Read(b[:])
		line <- string(b[:n])
	}()
	select {
	case s := <-line:
		var n int
		if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
			t.Skipf("Xvfb did not report a display (%q)", s)
		}
		return n
	case <-exited:
		t.Skip("Xvfb exited immediately; cannot run X11 tests here")
	case <-time.After(15 * time.Second):
		t.Fatal("Xvfb start timed out")
	}
	return 0
}

func TestFiles(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	p := filepath.Join(dir, "sub", "notes.txt")

	if msg := h.fail("remohop_write_file", map[string]any{"path": p, "content": "x"}); !strings.Contains(msg, "no such file") {
		t.Errorf("missing parent: %s", msg)
	}
	var w writeFileOut
	h.ok("remohop_write_file", map[string]any{"path": p, "content": "line1\r\nline2\n", "make_dirs": true, "mode": "0640"}, &w)
	if w.Bytes != 13 || w.Size != 13 {
		t.Fatalf("write: %+v", w)
	}
	if st, err := os.Stat(p); err != nil || (!onWindows && st.Mode().Perm() != 0o640) {
		t.Fatalf("mode: %v %v", st, err)
	}
	h.ok("remohop_write_file", map[string]any{"path": p, "content": "line3\n", "append": true}, &w)
	if w.Size != 19 {
		t.Fatalf("append: %+v", w)
	}

	var r readFileOut
	h.ok("remohop_read_file", map[string]any{"path": p}, &r)
	if r.Content != "line1\r\nline2\nline3\n" || r.More || r.Binary || r.Size != 19 {
		t.Fatalf("read: %+v", r)
	}
	h.ok("remohop_read_file", map[string]any{"path": p, "offset": 7, "limit": 5}, &r)
	if r.Content != "line2" || !r.More || r.BytesRead != 5 {
		t.Fatalf("ranged read: %+v", r)
	}

	// A multi-byte character cut by the limit is not mistaken for binary.
	multi := filepath.Join(dir, "multi.txt")
	os.WriteFile(multi, []byte("aé€b"), 0o644)
	h.ok("remohop_read_file", map[string]any{"path": multi, "limit": 4}, &r)
	if r.Binary || r.Content != "aé" || !r.More {
		t.Fatalf("partial rune: %+v", r)
	}

	bin := filepath.Join(dir, "blob")
	os.WriteFile(bin, []byte{0x7f, 'E', 'L', 'F', 0, 1, 2}, 0o644)
	r = readFileOut{}
	res := h.ok("remohop_read_file", map[string]any{"path": bin}, &r)
	if !r.Binary || r.Content != "" || !strings.Contains(text(res), "remohop_download") {
		t.Fatalf("binary: %+v / %s", r, text(res))
	}
	if runtime.GOOS == "linux" {
		// Special files report size 0 but have content.
		h.ok("remohop_read_file", map[string]any{"path": "/proc/self/status", "limit": 16}, &r)
		if r.BytesRead != 16 || !r.More {
			t.Fatalf("/proc read: %+v", r)
		}
	}

	if msg := h.fail("remohop_read_file", map[string]any{"path": dir}); !strings.Contains(msg, "remohop_list_dir") {
		t.Errorf("read dir: %s", msg)
	}
	if msg := h.fail("remohop_read_file", map[string]any{"path": filepath.Join(dir, "nope")}); !strings.Contains(msg, "no such file") {
		t.Errorf("read missing: %s", msg)
	}

	var ld listDirOut
	if err := os.Symlink("notes.txt", filepath.Join(dir, "sub", "link")); err != nil {
		// Unprivileged Windows accounts cannot create symlinks.
		h.ok("remohop_list_dir", map[string]any{"path": filepath.Join(dir, "sub")}, &ld)
		if ld.Total != 1 || ld.Entries[0].Name != "notes.txt" || ld.Entries[0].Type != "file" || ld.Entries[0].Size != 19 {
			t.Fatalf("list: %+v", ld)
		}
	} else {
		res = h.ok("remohop_list_dir", map[string]any{"path": filepath.Join(dir, "sub")}, &ld)
		if ld.Total != 2 || ld.Entries[0].Name != "link" || ld.Entries[0].Type != "symlink" || ld.Entries[0].Link != "notes.txt" ||
			ld.Entries[1].Name != "notes.txt" || ld.Entries[1].Type != "file" || ld.Entries[1].Size != 19 {
			t.Fatalf("list: %+v", ld)
		}
		if !strings.Contains(text(res), "link -> notes.txt") {
			t.Errorf("list text: %s", text(res))
		}
	}
	h.ok("remohop_list_dir", map[string]any{"path": dir, "limit": 1}, &ld)
	if !ld.Truncated || len(ld.Entries) != 1 || ld.Total < 3 {
		t.Fatalf("list limit: %+v", ld)
	}
}

func TestUploadDownload(t *testing.T) {
	h := newHarness(t)
	local, remote := t.TempDir(), t.TempDir()
	src := filepath.Join(local, "data.bin")
	payload := bytes.Repeat([]byte("0123456789abcdef"), 200_000) // 3.2 MB: takes the streaming path
	os.WriteFile(src, payload, 0o640)

	var tr transferOut
	h.ok("remohop_upload", map[string]any{"local_path": src, "remote_path": remote}, &tr) // into a directory
	dest := filepath.Join(remote, "data.bin")
	if tr.To != dest || tr.Bytes != int64(len(payload)) {
		t.Fatalf("upload: %+v", tr)
	}
	if b, _ := os.ReadFile(dest); !bytes.Equal(b, payload) {
		t.Fatal("uploaded content differs")
	}
	if msg := h.fail("remohop_upload", map[string]any{"local_path": src, "remote_path": dest}); !strings.Contains(msg, "overwrite") {
		t.Errorf("overwrite guard: %s", msg)
	}
	h.ok("remohop_upload", map[string]any{"local_path": src, "remote_path": dest, "overwrite": true}, &tr)

	back := filepath.Join(local, "back.bin")
	h.ok("remohop_download", map[string]any{"remote_path": dest, "local_path": back}, &tr)
	if b, _ := os.ReadFile(back); !bytes.Equal(b, payload) || tr.Bytes != int64(len(payload)) {
		t.Fatal("downloaded content differs")
	}
	if msg := h.fail("remohop_download", map[string]any{"remote_path": dest, "local_path": back}); !strings.Contains(msg, "overwrite") {
		t.Errorf("download overwrite guard: %s", msg)
	}
	into := t.TempDir()
	h.ok("remohop_download", map[string]any{"remote_path": dest, "local_path": into}, &tr) // into a directory
	if tr.To != filepath.Join(into, "data.bin") {
		t.Fatalf("download into dir: %+v", tr)
	}
	if msg := h.fail("remohop_download", map[string]any{"remote_path": remote, "local_path": filepath.Join(local, "x")}); !strings.Contains(msg, "not a regular file") {
		t.Errorf("download dir: %s", msg)
	}
	if msg := h.fail("remohop_upload", map[string]any{"local_path": "rel.txt", "remote_path": "/tmp/x"}); !strings.Contains(msg, "absolute") {
		t.Errorf("relative local: %s", msg)
	}
	if msg := h.fail("remohop_upload", map[string]any{"local_path": local, "remote_path": "/tmp/x"}); !strings.Contains(msg, "not a regular file") {
		t.Errorf("upload dir: %s", msg)
	}
	// A failed download leaves no temporary files behind.
	entries, _ := os.ReadDir(local)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".remohop-write-") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestLocalWritesNeverClobber(t *testing.T) {
	board := clip.NewMemoryBoard()
	clip.SetHostBoard(board)
	t.Cleanup(func() { clip.SetHostBoard(clip.Virtual()) })
	// save_path is checked before the remote machine is asked for anything,
	// so the screenshot cases need no display.
	h := newHarness(t)
	h.ok("remohop_clipboard_set", map[string]any{"text": "payload"}, nil)

	dir := t.TempDir()
	victim := filepath.Join(dir, "bashrc")
	os.WriteFile(victim, []byte("original"), 0o644)
	link := filepath.Join(dir, "link")
	dangling := filepath.Join(dir, "dangling")
	symlinks := os.Symlink(victim, link) == nil && os.Symlink(filepath.Join(dir, "newfile"), dangling) == nil
	if !symlinks {
		// Unprivileged Windows accounts cannot create symlinks; plain
		// files in their place still exercise the no-overwrite rule.
		os.WriteFile(link, []byte("plain"), 0o644)
		os.WriteFile(dangling, []byte("plain"), 0o644)
	}

	attempts := []struct {
		tool string
		args map[string]any
	}{
		{"remohop_clipboard_get", map[string]any{"save_path": victim}},
		{"remohop_clipboard_get", map[string]any{"save_path": link}},
		{"remohop_clipboard_get", map[string]any{"save_path": dangling}},
		{"remohop_screenshot", map[string]any{"save_path": victim}},
		{"remohop_screenshot", map[string]any{"save_path": link}},
	}
	for _, a := range attempts {
		if msg := h.fail(a.tool, a.args); !strings.Contains(msg, "overwrite") {
			t.Errorf("%s %v: %s", a.tool, a.args, msg)
		}
	}
	if b, _ := os.ReadFile(victim); string(b) != "original" {
		t.Fatalf("existing file was modified: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "newfile")); err == nil {
		t.Fatal("write followed a dangling symlink")
	}

	// A fresh path works and is private.
	fresh := filepath.Join(dir, "clip.txt")
	h.ok("remohop_clipboard_get", map[string]any{"save_path": fresh}, nil)
	if st, err := os.Stat(fresh); err != nil || (!onWindows && st.Mode().Perm() != 0o600) {
		t.Fatalf("fresh save: %v %v", st, err)
	}
	if !symlinks {
		return
	}
	// overwrite=true replaces the symlink itself and leaves its target alone.
	h.ok("remohop_clipboard_get", map[string]any{"save_path": link, "overwrite": true}, nil)
	if b, _ := os.ReadFile(victim); string(b) != "original" {
		t.Fatalf("overwrite followed the symlink: %q", b)
	}
	if b, _ := os.ReadFile(link); string(b) != "payload" {
		t.Fatalf("link not replaced: %q", b)
	}
	if st, _ := os.Lstat(link); st.Mode()&os.ModeSymlink != 0 {
		t.Fatal("link is still a symlink")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".remohop-write-") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestConfigDirIsOffLimits(t *testing.T) {
	cfg := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", cfg)
	h := newHarness(t)
	secret := filepath.Join(cfg, "peers.json")
	os.WriteFile(secret, []byte(`[{"token":"s3cret"}]`), 0o600)
	os.WriteFile(filepath.Join(cfg, "identity.key"), []byte("key"), 0o600)
	outside := t.TempDir()
	viaLink := filepath.Join(outside, "cfglink")
	if err := os.Symlink(cfg, viaLink); err != nil {
		viaLink = cfg // no symlinks for this account (Windows): the direct path stands in
	}
	remote := t.TempDir()
	remoteFile := filepath.Join(remote, "f")
	os.WriteFile(remoteFile, []byte("evil"), 0o644)

	for _, p := range []string{secret, filepath.Join(cfg, "identity.key"), filepath.Join(viaLink, "peers.json"), filepath.Join(cfg, "new-file")} {
		if msg := h.fail("remohop_upload", map[string]any{"local_path": p, "remote_path": filepath.Join(remote, "stolen")}); !strings.Contains(msg, "configuration directory") {
			t.Errorf("upload of %s: %s", p, msg)
		}
		if msg := h.fail("remohop_download", map[string]any{"remote_path": remoteFile, "local_path": p, "overwrite": true}); !strings.Contains(msg, "configuration directory") {
			t.Errorf("download to %s: %s", p, msg)
		}
		if msg := h.fail("remohop_clipboard_get", map[string]any{"save_path": p, "overwrite": true}); !strings.Contains(msg, "configuration directory") && !strings.Contains(msg, "empty") {
			t.Errorf("clipboard save to %s: %s", p, msg)
		}
		if msg := h.fail("remohop_screenshot", map[string]any{"save_path": p, "overwrite": true}); !strings.Contains(msg, "configuration directory") {
			t.Errorf("screenshot save to %s: %s", p, msg)
		}
	}
	if b, _ := os.ReadFile(secret); string(b) != `[{"token":"s3cret"}]` {
		t.Fatalf("credentials were modified: %q", b)
	}
	if _, err := os.Stat(filepath.Join(remote, "stolen")); err == nil {
		t.Fatal("credentials were uploaded")
	}
	if _, err := os.Stat(filepath.Join(cfg, "new-file")); err == nil {
		t.Fatal("file created in the config dir")
	}
	// Files next to it are fine.
	ok := filepath.Join(outside, "ok.txt")
	h.ok("remohop_download", map[string]any{"remote_path": remoteFile, "local_path": ok}, nil)
}

func TestClipboardHistoryByTargetOrAlias(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	hist, err := clip.OpenHistory("")
	if err != nil {
		t.Fatal(err)
	}
	add := func(peerName, text string) {
		if _, err := hist.Add(clip.Entry{Dir: clip.DirPush, Via: "set", Peer: peerName}, clip.Text(text)); err != nil {
			t.Fatal(err)
		}
	}
	add("123456789", "by-id")
	add("office", "by-alias")
	add("999999999", "other-machine")
	store, _ := dial.OpenStore("")
	store.Update("123456789", func(p *dial.Peer) error { p.Alias = "office"; return nil })

	h := newHarness(t)
	for _, target := range []string{"office", "123 456 789", "123456789"} {
		res := h.ok("remohop_clipboard_history", map[string]any{"target": target}, nil)
		got := text(res)
		if !strings.Contains(got, "by-id") || !strings.Contains(got, "by-alias") || strings.Contains(got, "other-machine") {
			t.Errorf("target %q: %s", target, got)
		}
	}
	res := h.ok("remohop_clipboard_history", nil, nil)
	if !strings.Contains(text(res), "other-machine") {
		t.Errorf("unfiltered history lost an entry: %s", text(res))
	}
}

func TestInput(t *testing.T) {
	display := startXvfb(t) // 640x480
	t.Setenv("DISPLAY", fmt.Sprintf(":%d", display))
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("XAUTHORITY", "")
	h := newHarness(t)

	// Before any screenshot there is nothing the coordinates could refer to.
	if msg := h.fail("remohop_click", map[string]any{"x": 10, "y": 10}); !strings.Contains(msg, "remohop_screenshot first") {
		t.Fatalf("click without screenshot: %s", msg)
	}

	// A half-size screenshot: image pixels map to twice the display pixels.
	var shot screenshotOut
	res := h.ok("remohop_screenshot", map[string]any{"max_width": 320}, &shot)
	if shot.Width != 320 || shot.Height != 240 || shot.DisplayWidth != 640 || shot.DisplayHeight != 480 ||
		shot.Shows != (rect{0, 0, 640, 480}) || !strings.Contains(text(res), "THIS image") {
		t.Fatalf("screenshot geometry: %+v / %s", shot, text(res))
	}
	var out inputOut
	res = h.ok("remohop_click", map[string]any{"x": 160, "y": 120}, &out)
	if out.Action != "click" || out.At == nil || *out.At != (point{321, 241}) || out.Pointer != (point{321, 241}) || out.Button != "left" || out.Clicks != 1 ||
		out.DisplayWidth != 640 || !strings.Contains(text(res), "verify") {
		t.Fatalf("scaled click: %+v / %s", out, text(res))
	}
	h.ok("remohop_click", map[string]any{"x": 0, "y": 0, "button": "right", "double": true}, &out)
	if *out.At != (point{1, 1}) || out.Button != "right" || out.Clicks != 2 {
		t.Fatalf("corner click: %+v", out)
	}
	if msg := h.fail("remohop_click", map[string]any{"x": 500, "y": 100}); !strings.Contains(msg, "outside the last screenshot") {
		t.Fatalf("out of image: %s", msg)
	}
	if msg := h.fail("remohop_click", map[string]any{"x": 1, "y": 1, "display": 0}); !strings.Contains(msg, "coordinate_space") {
		t.Fatalf("display in screenshot space: %s", msg)
	}

	// A region capture: image pixel (x,y) is display pixel (100+x, 50+y).
	h.ok("remohop_screenshot", map[string]any{"region": map[string]any{"x": 100, "y": 50, "width": 200, "height": 100}}, &shot)
	if shot.Width != 200 || shot.Height != 100 || shot.Shows != (rect{100, 50, 200, 100}) || shot.DisplayWidth != 640 {
		t.Fatalf("region geometry: %+v", shot)
	}
	h.ok("remohop_move", map[string]any{"x": 10, "y": 20}, &out)
	if out.Action != "move" || out.Pointer != (point{110, 70}) {
		t.Fatalf("move in region: %+v", out)
	}
	// A region that is also downscaled.
	h.ok("remohop_screenshot", map[string]any{"region": map[string]any{"x": 200, "y": 100, "width": 400, "height": 300}, "max_width": 100}, &shot)
	if shot.Width != 100 || shot.Height != 75 {
		t.Fatalf("scaled region geometry: %+v", shot)
	}
	h.ok("remohop_move", map[string]any{"x": 50, "y": 0}, &out)
	if out.Pointer != (point{402, 102}) {
		t.Fatalf("move in scaled region: %+v", out)
	}

	// Real display pixels on request.
	h.ok("remohop_move", map[string]any{"x": 600, "y": 400, "coordinate_space": "display"}, &out)
	if out.Pointer != (point{600, 400}) {
		t.Fatalf("display-space move: %+v", out)
	}
	h.ok("remohop_scroll", map[string]any{"dy": 3, "x": 5, "y": 5}, &out)
	if out.Action != "scroll" || out.DY != 3 || out.Pointer != (point{222, 122}) {
		t.Fatalf("scroll: %+v", out)
	}
	if msg := h.fail("remohop_scroll", map[string]any{}); !strings.Contains(msg, "dy") {
		t.Errorf("empty scroll: %s", msg)
	}
	if msg := h.fail("remohop_scroll", map[string]any{"dy": 1, "x": 5}); !strings.Contains(msg, "both x and y") {
		t.Errorf("half position: %s", msg)
	}

	h.ok("remohop_type", map[string]any{"text": "héllo wörld"}, &out)
	if out.Action != "type" || out.Chars != 11 {
		t.Fatalf("type: %+v", out)
	}
	h.ok("remohop_key", map[string]any{"keys": []string{"Control+A", "BackSpace"}, "repeat": 2}, &out)
	if out.Action != "key" || len(out.Keys) == 0 || out.Keys[0] != "ctrl+a" {
		t.Fatalf("key: %+v", out)
	}
	if msg := h.fail("remohop_key", map[string]any{"keys": []string{"ctrl+nosuchkey"}}); !strings.Contains(msg, "Return") {
		t.Errorf("bad key should list valid names: %s", msg)
	}
	if msg := h.fail("remohop_key", map[string]any{"keys": []string{}}); !strings.Contains(msg, "empty") {
		t.Errorf("no keys: %s", msg)
	}
	if msg := h.fail("remohop_type", map[string]any{"text": ""}); !strings.Contains(msg, "empty") {
		t.Errorf("empty text: %s", msg)
	}
	if msg := h.fail("remohop_click", map[string]any{"x": 1, "y": 1, "button": "side"}); !strings.Contains(msg, "left, right or middle") {
		t.Errorf("bad button: %s", msg)
	}
}

func TestViewMapping(t *testing.T) {
	v := view{imgW: 1568, imgH: 882, shows: rect{0, 0, 2560, 1440}}
	for _, c := range []struct{ x, y, wx, wy int }{{0, 0, 0, 0}, {1567, 881, 2559, 1439}, {784, 441, 1280, 720}} {
		x, y, err := v.toDisplay(c.x, c.y)
		if err != nil || x != c.wx || y != c.wy {
			t.Errorf("toDisplay(%d,%d) = %d,%d %v; want %d,%d", c.x, c.y, x, y, err, c.wx, c.wy)
		}
	}
	for _, p := range [][2]int{{-1, 0}, {1568, 0}, {0, 882}} {
		if _, _, err := v.toDisplay(p[0], p[1]); err == nil {
			t.Errorf("toDisplay(%v) accepted a point outside the image", p)
		}
	}
}

func TestFeatureRefusalReachesModel(t *testing.T) {
	h := newHarness(t)
	h.authorize = func(service string) error {
		return fmt.Errorf("the host does not allow %s (feature %q is switched off; its owner can allow it with `remohop config feature %s on`)", service, "x", "x")
	}
	calls := []struct {
		tool string
		args map[string]any
	}{
		{"remohop_exec", map[string]any{"command": sh.noop}},
		{"remohop_info", nil},
		{"remohop_screenshot", nil},
		{"remohop_click", map[string]any{"x": 1, "y": 1, "coordinate_space": "display"}},
		{"remohop_read_file", map[string]any{"path": "/etc/hostname"}},
		{"remohop_clipboard_get", nil},
		{"remohop_forward_start", map[string]any{"spec": "0:1", "reverse": true}},
		{"remohop_wake", map[string]any{"mac": "aa:bb:cc:dd:ee:ff"}},
	}
	for _, c := range calls {
		msg := h.fail(c.tool, c.args)
		if !strings.Contains(msg, "remohop config feature x on") || !strings.Contains(msg, "does not allow") {
			t.Errorf("%s: refusal not passed on verbatim: %s", c.tool, msg)
		}
		if strings.Contains(msg, "headless") || strings.Contains(msg, "pair") {
			t.Errorf("%s: misleading advice added to a refusal: %s", c.tool, msg)
		}
	}
	// Refusals are answers, not connection failures: one session, no retries.
	if n := h.dials.Load(); n != 1 {
		t.Fatalf("refused calls dialled %d times, want 1", n)
	}
}

func TestSend(t *testing.T) {
	if onWindows {
		t.Skip("the Downloads known folder cannot be redirected for a test on Windows")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	cfg := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", cfg)
	h := newHarness(t)

	src := t.TempDir()
	a := filepath.Join(src, "report.txt")
	os.WriteFile(a, []byte("v1"), 0o644)
	os.MkdirAll(filepath.Join(src, "tree", "sub"), 0o755)
	os.WriteFile(filepath.Join(src, "tree", "sub", "f"), []byte("deep"), 0o644)

	var out sendOut
	h.ok("remohop_send", map[string]any{"local_paths": []string{a}}, &out)
	if len(out.Sent) != 1 || out.Sent[0].Bytes != 2 || filepath.Base(out.Sent[0].Path) != "report.txt" {
		t.Fatalf("send: %+v", out)
	}
	first := out.Sent[0].Path
	os.WriteFile(a, []byte("v2!"), 0o644)
	res := h.ok("remohop_send", map[string]any{"local_paths": []string{a}}, &out)
	if out.Sent[0].Path == first || !strings.Contains(text(res), out.Sent[0].Path) {
		t.Fatalf("second send reused the name: %+v", out)
	}
	if b, _ := os.ReadFile(first); string(b) != "v1" {
		t.Fatalf("first delivery was overwritten: %q", b)
	}
	if b, _ := os.ReadFile(out.Sent[0].Path); string(b) != "v2!" {
		t.Fatalf("second delivery: %q", b)
	}

	tree := filepath.Join(src, "tree")
	if msg := h.fail("remohop_send", map[string]any{"local_paths": []string{tree}}); !strings.Contains(msg, "recursive") {
		t.Errorf("directory without recursive: %s", msg)
	}
	h.ok("remohop_send", map[string]any{"local_paths": []string{tree}, "recursive": true}, &out)
	if len(out.Sent) != 1 || !out.Sent[0].Dir {
		t.Fatalf("recursive send: %+v", out)
	}
	if b, _ := os.ReadFile(filepath.Join(out.Sent[0].Path, "sub", "f")); string(b) != "deep" {
		t.Fatalf("tree content: %q", b)
	}

	os.WriteFile(filepath.Join(cfg, "peers.json"), []byte("[]"), 0o600)
	for _, p := range []string{filepath.Join(cfg, "peers.json"), cfg} {
		if msg := h.fail("remohop_send", map[string]any{"local_paths": []string{p}, "recursive": true}); !strings.Contains(msg, "configuration directory") {
			t.Errorf("send of %s: %s", p, msg)
		}
	}
	if msg := h.fail("remohop_send", map[string]any{"local_paths": []string{"rel.txt"}}); !strings.Contains(msg, "absolute") {
		t.Errorf("relative: %s", msg)
	}
	if msg := h.fail("remohop_send", map[string]any{"local_paths": []string{}}); !strings.Contains(msg, "empty") {
		t.Errorf("no paths: %s", msg)
	}
}

func TestWake(t *testing.T) {
	// A stand-in host: the real service would broadcast on this machine's
	// networks.
	type wakeReq struct {
		MAC  string `json:"mac"`
		Port int    `json:"port"`
	}
	reqs := make(chan wakeReq, 4)
	reg := peer.NewRegistry()
	reg.Handle("wake", func(_ context.Context, _ *peer.Conn, s peer.Stream, raw json.RawMessage) error {
		var req wakeReq
		if err := json.Unmarshal(raw, &req); err != nil {
			return err
		}
		reqs <- req
		return wire.WriteJSON(s, map[string]any{"mac": req.MAC, "port": req.Port, "targets": []string{"192.168.1.255:9"}})
	})
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	h := newHarnessWith(t, reg)
	var out wakeOut
	res := h.ok("remohop_wake", map[string]any{"mac": "AA-BB-CC-DD-EE-FF"}, &out)
	got := <-reqs
	if got.MAC != "aa:bb:cc:dd:ee:ff" || out.MAC != "aa:bb:cc:dd:ee:ff" || len(out.SentTo) != 1 || !strings.Contains(text(res), "not confirmed") {
		t.Fatalf("wake: sent %+v, got %+v / %s", got, out, text(res))
	}
	store, err := wake.OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("nas", "11:22:33:44:55:66"); err != nil {
		t.Fatal(err)
	}
	h.ok("remohop_wake", map[string]any{"mac": "nas", "port": 7}, &out)
	if got = <-reqs; got.MAC != "11:22:33:44:55:66" || got.Port != 7 {
		t.Fatalf("saved name: %+v", got)
	}
	if msg := h.fail("remohop_wake", map[string]any{"mac": "printer"}); !strings.Contains(msg, "remohop wake --save") {
		t.Errorf("unknown name: %s", msg)
	}
}
