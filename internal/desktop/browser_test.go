package desktop

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/signal"
)

// testSignal runs a rendezvous server on loopback.
func testSignal(t *testing.T) string {
	t.Helper()
	srv, err := signal.NewServer(signal.Config{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	main, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	alt, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, main, alt) }()
	t.Cleanup(func() { cancel(); <-done; srv.Close() })
	return main.LocalAddr().String()
}

// startApp runs an App and its UI server on a fresh configuration.
func startApp(t *testing.T, sig string) (*App, *Server) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", dir)
	app, err := NewApp(Config{Signal: sig, AskTimeout: 20 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	srv, err := NewServer(app, nil)
	if err != nil {
		t.Fatal(err)
	}
	app.cfg.UIOrigin = srv.Origin()
	go srv.Serve()
	if err := app.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Stop(); srv.Close() })
	return app, srv
}

// TestPageInBrowser runs testdata/page_test.mjs: the real page in headless
// Chrome, against a real App with its own rendezvous server and a helper
// that dials in: the first-run wizard, incoming connections, the approval
// dialog (new device, scam warning, view only by default, deny, allow with
// control), changing a live session's access, the address book, a host in a
// tab (the embedded console logs in and is approved), closing it, Settings,
// dark theme, Chinese and a small window. Needs node and Chrome:
// REMOHOP_BROWSER_TEST=1; REMOHOP_SHOTS=/dir keeps screenshots.
func TestPageInBrowser(t *testing.T) {
	if os.Getenv("REMOHOP_BROWSER_TEST") != "1" {
		t.Skip("set REMOHOP_BROWSER_TEST=1 (needs node and google-chrome)")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node")
	}
	sig := testSignal(t)
	app, srv := startApp(t, "")

	var mu sync.Mutex
	result := ""
	var helper *peer.Conn
	dirs := map[string]string{} // a device keeps its identity
	ctl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dial":
			st := app.State().Incoming
			id, pw := strings.ReplaceAll(st.ID, " ", ""), st.Password
			name := r.URL.Query().Get("name")
			config.Set(hostd.SettingName, name)
			mu.Lock()
			result = "waiting"
			if dirs[name] == "" {
				dirs[name] = t.TempDir()
			}
			dir := dirs[name]
			mu.Unlock()
			go func() {
				c, err := dial.Dial(context.Background(), id, dial.Options{Dir: dir, Password: pw, Signal: sig})
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					result = "denied: " + err.Error()
					return
				}
				helper, result = c, "connected"
			}()
		case "/result":
			for i := 0; i < 100; i++ {
				mu.Lock()
				r := result
				mu.Unlock()
				if r != "waiting" {
					fmt.Fprint(w, r)
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
			fmt.Fprint(w, "timeout")
		case "/features":
			h, err := app.running()
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			for _, s := range h.Sessions() {
				all, fs := s.Features()
				fmt.Fprintln(w, all, fs)
			}
		case "/savepeer":
			id := strings.ReplaceAll(app.State().Incoming.ID, " ", "")
			st, err := dial.OpenStore("")
			if err == nil {
				err = st.Update(id, func(*dial.Peer) error { return nil })
			}
			if err == nil {
				err = st.SetAlias(id, "Office-PC")
			}
			if err != nil {
				http.Error(w, err.Error(), 500)
			}
			app.invalidatePeers()
		default:
			http.NotFound(w, r)
		}
	}))
	defer ctl.Close()
	defer func() {
		mu.Lock()
		if helper != nil {
			helper.Close()
		}
		mu.Unlock()
	}()
	script, _ := filepath.Abs(filepath.Join("testdata", "page_test.mjs"))
	cmd := exec.Command(node, script)
	cmd.Env = append(os.Environ(), "REMOHOP_APP_URL="+srv.URL(), "REMOHOP_CONTROL_URL="+ctl.URL, "REMOHOP_SIGNAL_ADDR="+sig)
	out, err := cmd.CombinedOutput()
	t.Logf("\n%s", out)
	if err != nil {
		t.Fatalf("page_test.mjs: %v", err)
	}
}
