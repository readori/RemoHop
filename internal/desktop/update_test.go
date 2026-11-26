package desktop

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/update"
)

func TestUpdateCheck(t *testing.T) {
	t.Setenv(NoUpdateCheckEnv, "")

	var latest atomic.Value
	latest.Store("v1.0.2")
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"version":"` + latest.Load().(string) + `"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", dir)
	a, err := NewApp(Config{UpdateURL: srv.URL, UpdateClient: srv.Client(), UpdateDelay: 10 * time.Millisecond, UpdateEvery: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	setVersion := func(v string) { a.mu.Lock(); a.upd.current = v; a.mu.Unlock() }
	setVersion("v1.0.1")
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	defer a.Stop()

	// The first check runs by itself shortly after start.
	waitFor(t, "the first check", func() bool { return a.State().Update.Latest != "" })
	u := a.State().Update
	if !u.Auto || !u.Available || u.Skipped || u.Latest != "v1.0.2" || u.Current != "v1.0.1" || u.Checked == 0 || u.Error != "" {
		t.Fatalf("after the first check: %+v", u)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d requests; the loop must not check again within the interval", n)
	}

	// Skipping hides this version only.
	if err := a.SkipUpdate("v1.0.2"); err != nil {
		t.Fatal(err)
	}
	if u := a.State().Update; !u.Skipped {
		t.Fatalf("after skip: %+v", u)
	}
	if err := a.SkipUpdate("../x"); err == nil {
		t.Fatal("a bad version was accepted")
	}
	latest.Store("v1.0.3")
	a.CheckUpdate(context.Background())
	if u := a.State().Update; !u.Available || u.Skipped || u.Latest != "v1.0.3" {
		t.Fatalf("a newer version than the skipped one: %+v", u)
	}

	// The pages are built here, from the version alone.
	if l, _ := a.UpdateLink("notes", ""); l != "https://github.com/readori/RemoHop/releases/tag/v1.0.3" {
		t.Fatalf("notes: %s", l)
	}
	if l, _ := a.UpdateLink("download", "zh"); l != "https://remohop.com/download/" {
		t.Fatalf("download: %s", l)
	}
	if _, err := a.UpdateLink("https://evil.example/", ""); err == nil {
		t.Fatal("an arbitrary page was accepted")
	}

	// Up to date, then a broken answer.
	setVersion("v1.0.3")
	if u := a.State().Update; u.Available {
		t.Fatalf("same version: %+v", u)
	}
	latest.Store("nonsense")
	a.CheckUpdate(context.Background())
	if u := a.State().Update; u.Error != "check_failed" || u.Latest != "v1.0.3" {
		t.Fatalf("after a bad answer: %+v", u)
	}

	// The setting persists; off by default never.
	if err := a.SetUpdateAuto(false); err != nil {
		t.Fatal(err)
	}
	b, _ := NewApp(Config{})
	if b.State().Update.Auto || b.updateAuto() {
		t.Fatal("switching the check off did not persist")
	}
	t.Setenv(NoUpdateCheckEnv, "1")
	if err := a.SetUpdateAuto(true); err != nil {
		t.Fatal(err)
	}
	if a.updateAuto() || !a.State().Update.Managed {
		t.Fatal(NoUpdateCheckEnv + " did not switch the check off")
	}

	// A development build is never out of date.
	setVersion("dev")
	latest.Store("v9.0.0")
	a.CheckUpdate(context.Background())
	if u := a.State().Update; u.Available {
		t.Fatalf("dev build: %+v", u)
	}
}

// "Upgrade now": the signed release's archive replaces the program, the
// new one is started and the old one quits; nothing happens while someone
// is connected, and a release with a bad signature changes nothing.
func TestInstallUpdate(t *testing.T) {
	t.Setenv(NoUpdateCheckEnv, "")
	pub, priv, _ := ed25519.GenerateKey(nil)
	dir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	exe := filepath.Join(dir, "remohop-app")
	os.WriteFile(exe, []byte("v1.0.2"), 0o755)

	// A release: the archive, SHA256SUMS and its signature.
	var tgzBuf bytes.Buffer
	gz := gzip.NewWriter(&tgzBuf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "remohop-app", Mode: 0o755, Size: 6, Typeflag: tar.TypeReg})
	tw.Write([]byte("v1.0.3"))
	tw.Close()
	gz.Close()
	sum := sha256.Sum256(tgzBuf.Bytes())
	sums := hex.EncodeToString(sum[:]) + "  remohop-app_linux_amd64.tar.gz\n"
	files := map[string][]byte{
		"/v1.0.3/version.json":                   []byte(`{"version":"v1.0.3"}`),
		"/v1.0.3/SHA256SUMS":                     []byte(sums),
		"/v1.0.3/SHA256SUMS.ed25519":             update.Sign(priv, "v1.0.3", []byte(sums)),
		"/v1.0.3/remohop-app_linux_amd64.tar.gz": tgzBuf.Bytes(),
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	defer srv.Close()

	restarted := make(chan update.Plan, 1)
	plan := update.Plan{Asset: "remohop-app_linux_amd64.tar.gz", Member: "remohop-app", Target: exe}
	a, err := NewApp(Config{UpdateURL: srv.URL + "/v1.0.3/version.json", UpdateClient: srv.Client(), UpdateBase: srv.URL + "/%s",
		UpdateKeys: []ed25519.PublicKey{pub}, UpdatePlan: &plan, NoUpdateCheck: true,
		UpdateRestart: func(p update.Plan) error { restarted <- p; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	a.mu.Lock()
	a.upd.current = "v1.0.2"
	a.mu.Unlock()
	if a.State().Update.CanInstall {
		t.Fatal("can install before a newer release is known")
	}
	a.CheckUpdate(context.Background())
	if u := a.State().Update; !u.CanInstall {
		t.Fatalf("cannot install: %+v", u)
	}
	quit := make(chan struct{})
	if err := a.InstallUpdate(func() { close(quit) }); err != nil {
		t.Fatal(err)
	}
	select {
	case <-quit:
	case <-time.After(15 * time.Second):
		t.Fatalf("no restart: %+v", a.State().Update)
	}
	if p := <-restarted; p.Target != exe {
		t.Fatalf("restarted %+v", p)
	}
	if b, _ := os.ReadFile(exe); string(b) != "v1.0.3" {
		t.Fatalf("program after the upgrade: %q", b)
	}
	if u := a.State().Update; u.Stage != "restarting" {
		t.Fatalf("stage %q", u.Stage)
	}

	// A tampered signature: refused, the program stays.
	files["/v1.0.3/SHA256SUMS.ed25519"] = update.Sign(priv, "v1.0.4", []byte(sums))
	os.WriteFile(exe, []byte("v1.0.2"), 0o755)
	a.mu.Lock()
	a.upd.stage = ""
	a.mu.Unlock()
	if err := a.InstallUpdate(nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the refusal", func() bool { return a.State().Update.InstallError != "" })
	if b, _ := os.ReadFile(exe); string(b) != "v1.0.2" {
		t.Fatalf("a bad release changed the program: %q", b)
	}

	// Without trusted keys there is no one-click upgrade.
	a.cfg.UpdateKeys = []ed25519.PublicKey{}
	if a.State().Update.CanInstall {
		t.Fatal("can install without trusted keys")
	}
}

func TestEditDeviceAndPages(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	a, err := NewApp(Config{NoUpdateCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	a.update(func(s *Settings) {
		s.Trusted = append(s.Trusted, TrustedDevice{Identity: "k1", Name: "old", Control: false, Files: true})
	})
	if err := a.EditDevice("k1", "  iPhone 15 ", true, false); err != nil {
		t.Fatal(err)
	}
	st := a.Settings()
	if d, ok := st.trusted("k1"); !ok || d.Name != "iPhone 15" || !d.Control || d.Files {
		t.Fatalf("after edit: %+v", d)
	}
	if err := a.EditDevice("nope", "x", true, true); err == nil {
		t.Fatal("an unknown device was edited")
	}
	if u, _ := sitePage("privacy", "zh"); u != "https://remohop.com/privacy/" {
		t.Fatalf("privacy zh: %s", u)
	}
	if u, _ := sitePage("terms", "en"); u != "https://remohop.com/en/terms/" {
		t.Fatalf("terms en: %s", u)
	}
	if u, _ := sitePage("telegram", "zh"); u != "https://t.me/remohop" {
		t.Fatalf("telegram: %s", u)
	}
	if _, err := sitePage("https://evil.example", "en"); err == nil {
		t.Fatal("an arbitrary page was accepted")
	}
}
