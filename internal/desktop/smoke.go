package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/signal"
)

// The smoke test (--smoke DIR, for CI on each platform): the app runs in
// its real window with a throw-away configuration (REMOHOP_CONFIG_DIR), a
// rendezvous server of its own on loopback and a simulated helper on the
// same machine, and walks through its screens. Before each screen it
// writes the screen's name to DIR/step and waits (up to 20 seconds) for
// DIR/step.done, which the CI job creates once it has taken a screenshot.
// DIR/smoke.json records what happened.

func (s *Server) setSmokeView(v string) {
	s.mu.Lock()
	s.smokeView = v
	s.mu.Unlock()
	s.app.wake()
}

func (s *Server) smoke() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.smokeView
}

type smokeLog struct {
	Steps  []string `json:"steps"`
	Errors []string `json:"errors,omitempty"`
	UI     UIInfo   `json:"ui"`
}

func runSmoke(ctx context.Context, env *cli.Env, app *App, srv *Server, o runOptions) int {
	if os.Getenv("REMOHOP_CONFIG_DIR") == "" {
		env.Errorf("app --smoke needs REMOHOP_CONFIG_DIR (a throw-away configuration)")
		return cli.ExitUsage
	}
	dir := o.smoke
	if err := os.MkdirAll(dir, 0o755); err != nil {
		env.Errorf("app: %v", err)
		return cli.ExitError
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var log smokeLog
	fail := func(format string, args ...any) {
		log.Errors = append(log.Errors, fmt.Sprintf(format, args...))
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			log.UI = srv.shell().Info()
			b, _ := json.MarshalIndent(log, "", "  ")
			os.WriteFile(filepath.Join(dir, "smoke.json"), b, 0o644)
			srv.shell().Quit()
			cancel()
		}()
		step := func(name string) {
			log.Steps = append(log.Steps, name)
			time.Sleep(2500 * time.Millisecond) // let the page render
			os.Remove(filepath.Join(dir, "step.done"))
			os.WriteFile(filepath.Join(dir, "step"), []byte(name), 0o644)
			deadline := time.Now().Add(20 * time.Second)
			for time.Now().Before(deadline) {
				if _, err := os.Stat(filepath.Join(dir, "step.done")); err == nil {
					return
				}
				time.Sleep(200 * time.Millisecond)
			}
		}
		// Wait for the window's page.
		deadline := time.Now().Add(60 * time.Second)
		for {
			if v, _, _ := srv.Presence(); v > 0 {
				break
			}
			if time.Now().After(deadline) {
				fail("no page connected")
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		step("wizard")

		sig, err := smokeSignal(ctx)
		if err != nil {
			fail("rendezvous server: %v", err)
			return
		}
		if err := app.FinishSetup(serverCustom, sig, ""); err != nil {
			fail("setup: %v", err)
		}
		if err := app.SetIncoming(true); err != nil {
			fail("incoming: %v", err)
		}
		srv.setSmokeView("computer")
		var id, pw string
		for i := 0; i < 100; i++ {
			st := app.State()
			if st.Incoming.Phase == PhaseReady && st.Incoming.Password != "" {
				id, pw = strings.ReplaceAll(st.Incoming.ID, " ", ""), st.Incoming.Password
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if id == "" {
			fail("the host did not get an ID: %+v", app.State().Incoming)
		}
		step("computer")

		// A helper asks to connect.
		helperDir, _ := os.MkdirTemp("", "remohop-smoke-helper-")
		defer os.RemoveAll(helperDir)
		// The helper runs in this process, whose device name (the
		// config's) is what it calls itself.
		config.Set(hostd.SettingName, "Alex-Laptop")
		conns := make(chan error, 1)
		var helper interface{ Close() error }
		go func() {
			c, err := dial.Dial(ctx, id, dial.Options{Dir: helperDir, Password: pw, Signal: sig})
			if err == nil {
				helper = c
			}
			conns <- err
		}()
		var n int
		for i := 0; i < 100 && n == 0; i++ {
			if r := app.State().Incoming.Requests; len(r) > 0 {
				n = r[0].N
			}
			time.Sleep(200 * time.Millisecond)
		}
		if n == 0 {
			fail("no connection request arrived")
		}
		step("request")
		if n != 0 {
			if err := app.Decide(n, true, false, false, false); err != nil {
				fail("decide: %v", err)
			}
			if err := <-conns; err != nil {
				fail("helper: %v", err)
			}
		}
		step("session")

		// The address book: this computer saved under a name, and its tab.
		if id != "" {
			if st, err := dial.OpenStore(""); err == nil {
				st.Update(id, func(p *dial.Peer) error { return nil })
				st.SetAlias(id, "Office-PC")
			}
			app.invalidatePeers()
		}
		srv.setSmokeView("connect")
		step("connect")
		if id != "" {
			srv.setSmokeView("open:" + id)
			step("console")
		}
		srv.setSmokeView("settings")
		step("settings")
		app.SetTheme("dark")
		srv.setSmokeView("computer")
		step("computer-dark")
		app.SetLang("zh")
		step("computer-zh")
		app.SetTheme("")
		app.SetLang("")
		if helper != nil {
			helper.Close()
		}
	}()
	var err error
	if o.browser {
		err = runBrowser(ctx, app, srv, o, app.log)
	} else {
		err = runNative(ctx, app, srv, o, app.log)
	}
	if err != nil {
		fmt.Fprintf(env.Stderr, "app --smoke: %v\n", err)
		cancel()
		return cli.ExitError
	}
	<-done
	if len(log.Errors) > 0 {
		fmt.Fprintf(env.Stderr, "app --smoke: %s\n", strings.Join(log.Errors, "; "))
		return cli.ExitError
	}
	return cli.ExitOK
}

// smokeSignal runs a rendezvous server on loopback for the smoke test.
func smokeSignal(ctx context.Context) (string, error) {
	data, err := os.MkdirTemp("", "remohop-smoke-signal-")
	if err != nil {
		return "", err
	}
	srv, err := signal.NewServer(signal.Config{DataDir: data})
	if err != nil {
		return "", err
	}
	main, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	alt, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	go func() {
		srv.Serve(ctx, main, alt)
		srv.Close()
		os.RemoveAll(data)
	}()
	return main.LocalAddr().String(), nil
}
