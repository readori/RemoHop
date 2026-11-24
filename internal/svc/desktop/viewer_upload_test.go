package desktop

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

func uploadViewer(t *testing.T, opts Options, host peertest.Options) (*viewer, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("USERPROFILE", home)
	client, _ := peertest.PairOpts(t, peer.HostServices, host)
	v, err := newViewer(client, opts, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go v.serve()
	t.Cleanup(v.close)
	return v, home
}

func postUpload(t *testing.T, v *viewer, name string, body []byte, hdr map[string]string) (int, uploadResult) {
	t.Helper()
	req, err := http.NewRequest("POST", v.URL()+"upload?name="+urlEscape(name), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", strings.TrimSuffix(strings.Split(v.URL(), v.token)[0], "/"))
	req.Header.Set("X-RemoHop-Upload", "1")
	for k, val := range hdr {
		if val == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, val)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var r uploadResult
	b, _ := io.ReadAll(resp.Body)
	json.Unmarshal(b, &r)
	return resp.StatusCode, r
}

func urlEscape(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_' {
			b.WriteByte(c)
		} else {
			b.WriteString("%" + strings.ToUpper(string("0123456789abcdef"[c>>4])+string("0123456789abcdef"[c&15])))
		}
	}
	return b.String()
}

func TestViewerUpload(t *testing.T) {
	v, home := uploadViewer(t, Options{}, peertest.Options{})
	data := bytes.Repeat([]byte("remohop upload "), 100_000) // 1.4 MB: more than one chunk
	code, r := postUpload(t, v, "notes.txt", data, nil)
	downloads := filepath.Join(home, "Downloads")
	if code != 200 || r.Name != "notes.txt" || r.Size != int64(len(data)) || r.Path != downloads+"/notes.txt" {
		t.Fatalf("upload: %d %+v", code, r)
	}
	if got, err := os.ReadFile(filepath.Join(downloads, "notes.txt")); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("file on the host: %d bytes, %v", len(got), err)
	}
	// Nothing is ever overwritten.
	code, r = postUpload(t, v, "notes.txt", []byte("second"), nil)
	if code != 200 || r.Name != "notes (1).txt" {
		t.Fatalf("second upload: %d %+v", code, r)
	}
	if got, _ := os.ReadFile(filepath.Join(downloads, "notes.txt")); !bytes.Equal(got, data) {
		t.Fatal("the first file was overwritten")
	}
	// Empty files are files too.
	if code, r = postUpload(t, v, "empty", nil, nil); code != 200 || r.Size != 0 {
		t.Fatalf("empty upload: %d %+v", code, r)
	}
	// Names cannot leave the folder, whatever the page sends.
	for _, name := range []string{"../../escape.txt", "..\\..\\escape.txt", "/etc/escape.txt", "a/b/c.txt"} {
		code, r := postUpload(t, v, name, []byte("x"), nil)
		if code != 200 || strings.ContainsAny(r.Name, `/\`) {
			t.Fatalf("%q: %d %+v", name, code, r)
		}
		if _, err := os.Stat(filepath.Join(home, r.Name)); err == nil {
			t.Fatalf("%q escaped the Downloads folder", name)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(home), "escape.txt")); err == nil {
		t.Fatal("a file escaped")
	}
	for _, name := range []string{"", "..", ".", "...", " "} {
		if code, r := postUpload(t, v, name, []byte("x"), nil); code != http.StatusBadRequest {
			t.Errorf("%q accepted: %d %+v", name, code, r)
		}
	}
}

func TestViewerUploadGuards(t *testing.T) {
	v, home := uploadViewer(t, Options{}, peertest.Options{})
	for _, tc := range []struct {
		what string
		hdr  map[string]string
	}{
		{"foreign origin", map[string]string{"Origin": "https://evil.example"}},
		{"no origin", map[string]string{"Origin": ""}},
		{"no custom header", map[string]string{"X-RemoHop-Upload": ""}},
	} {
		if code, _ := postUpload(t, v, "x.txt", []byte("x"), tc.hdr); code != http.StatusForbidden {
			t.Errorf("%s: %d", tc.what, code)
		}
	}
	// GET is not an upload; a wrong token is not found.
	resp, err := http.Get(v.URL() + "upload?name=x")
	if err != nil || resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET: %v %v", resp, err)
	}
	if resp, err = http.Post(strings.Replace(v.URL(), v.token, "wrongtoken", 1)+"upload?name=x", "text/plain", strings.NewReader("x")); err != nil || resp.StatusCode != 404 {
		t.Errorf("bad token: %v %v", resp, err)
	}
	if _, err := os.Stat(filepath.Join(home, "Downloads")); err == nil {
		t.Fatal("a rejected request created the Downloads folder")
	}

	// A view-only session uploads nothing.
	vo, _ := uploadViewer(t, Options{ViewOnly: true}, peertest.Options{})
	if code, r := postUpload(t, vo, "x.txt", []byte("x"), nil); code != http.StatusForbidden || !strings.Contains(r.Error, "view-only") {
		t.Errorf("view-only: %d %+v", code, r)
	}
}

// --upload-dir sends files elsewhere on the host, and a host whose file
// feature is off says so.
func TestViewerUploadDirAndRefusal(t *testing.T) {
	v, _ := uploadViewer(t, Options{}, peertest.Options{})
	dir := t.TempDir()
	v.uploadDir = dir
	if code, r := postUpload(t, v, "a.bin", []byte("abc"), nil); code != 200 || r.Path != dir+"/a.bin" {
		t.Fatalf("custom dir: %d %+v", code, r)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.bin")); string(b) != "abc" {
		t.Fatalf("content %q", b)
	}
	off, _ := uploadViewer(t, Options{}, peertest.Options{Authorize: func(service string) error {
		if strings.HasPrefix(service, "fs") {
			return errors.New("the host does not allow " + service + " (feature \"file\" is switched off)")
		}
		return nil
	}})
	code, r := postUpload(t, off, "a.bin", []byte("abc"), nil)
	if code != http.StatusBadGateway || !strings.Contains(r.Error, "switched off") {
		t.Fatalf("refused: %d %+v", code, r)
	}
}

func TestUploadName(t *testing.T) {
	for in, want := range map[string]string{
		"report.pdf":      "report.pdf",
		"a:b*c?.txt":      "a_b_c_.txt",
		"dir/file":        "dir_file",
		"trailing. .":     "trailing",
		"tab\tname":       "tab_name",
		"naïve – 日本語.txt": "naïve – 日本語.txt",
	} {
		if got, err := uploadName(in); err != nil || got != want {
			t.Errorf("uploadName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	long := strings.Repeat("é", 200) + ".tar.gz"
	if got, err := uploadName(long); err != nil || len(got) > 255 || !strings.HasSuffix(got, ".gz") {
		t.Errorf("long name: %d bytes, %v", len(got), err)
	}
	for _, bad := range []string{"", ".", "..", "...", "\xff\xfe"} {
		if _, err := uploadName(bad); err == nil {
			t.Errorf("uploadName(%q) accepted", bad)
		}
	}
}
