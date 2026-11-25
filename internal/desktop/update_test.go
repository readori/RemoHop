package desktop

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
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
