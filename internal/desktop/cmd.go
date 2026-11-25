package desktop

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/localui"
	"github.com/readori/RemoHop/internal/quick"
)

const usage = `Usage: remohop app [--browser] [--hidden] [--no-open] [--lang zh|en]

The RemoHop desktop app: one window to connect to other computers and to let
others connect to this one.

  Connect        type a 9-digit ID, a saved name or host:port, or pick a
                 saved host (with whether it is online, and Wake-on-LAN where
                 a MAC address is saved); each host opens in a tab of its own
                 with the remote desktop, terminal, files, clipboard and
                 system information
  This computer  this computer's ID and temporary password, the switch that
                 lets others connect (off until you turn it on), who is
                 connected, and unattended access
  Settings       the rendezvous server, language, theme, start at login

Every incoming connection asks first, in a dialog that says whether the
device is new and what it may do: watch only (the default for a device
you do not know), control the mouse and keyboard, transfer files. A device
can be remembered so that it is let in with the same choice next time. A
banner stays on screen while anyone is connected.

On Windows, macOS and Linux with WebKitGTK the app has a window of its own
and a tray icon; elsewhere, or with --browser, it opens in a browser.
Starting it again shows the window of the one that runs.

Flags:
  --browser      use a browser window even where a native one is possible
  --hidden       start in the tray without showing the window (start at login)
  --no-open      print the page's address instead of opening a window
  --lang zh|en   language (default: the one chosen in Settings, else the
                 system's)

The first start asks which rendezvous server to use: the official one
(signal.remohop.com) or your own. Without one, computers are reached by
address (host:port).
`

func init() {
	cli.Register(&cli.Command{Name: "app", Summary: "the desktop app: connect to other computers and let others connect", Usage: usage, Run: run})
}

// urlFile records the running app's page address (0600) so that a second
// start can ask it to show its window.
const urlFile = "desktop.url"

// Options for runApp, from the flags.
type runOptions struct {
	browser, hidden, noOpen bool
	lang                    string
	smoke                   string
	appBrowser              string
}

type tolerant struct{ w io.Writer }

func (t tolerant) Write(p []byte) (int, error) {
	t.w.Write(p)
	return len(p), nil
}

func openLog(dir string) io.WriteCloser {
	path := filepath.Join(dir, "desktop.log")
	if st, err := os.Stat(path); err == nil && st.Size() > 1<<20 {
		os.Remove(path)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return nil
	}
	return f
}

func run(ctx context.Context, env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("app", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var o runOptions
	fs.BoolVar(&o.browser, "browser", false, "")
	fs.BoolVar(&o.hidden, "hidden", false, "")
	fs.BoolVar(&o.noOpen, "no-open", false, "")
	fs.StringVar(&o.lang, "lang", "", "")
	fs.StringVar(&o.smoke, "smoke", "", "")
	fs.StringVar(&o.appBrowser, "app-browser", os.Getenv("REMOHOP_QUICK_APP_BROWSER"), "")
	if err := fs.Parse(args); err != nil {
		env.Errorf("app: %v\n%s", err, usage)
		return cli.ExitUsage
	}
	if fs.NArg() != 0 {
		env.Errorf("app: unexpected argument %q", fs.Arg(0))
		return cli.ExitUsage
	}
	if o.lang != "" && normLang(o.lang) == "" {
		env.Errorf("app: --lang is zh or en")
		return cli.ExitUsage
	}
	dir, err := config.Dir()
	if err != nil {
		env.Errorf("app: %v", err)
		return cli.ExitError
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		env.Errorf("app: %v", err)
		return cli.ExitError
	}
	level := slog.LevelInfo
	if env.Globals.Verbose {
		level = slog.LevelDebug
	}
	var logOut io.Writer = tolerant{env.Stderr}
	if f := openLog(dir); f != nil {
		defer f.Close()
		logOut = io.MultiWriter(f, logOut)
	}
	log := slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: level}))

	// One app per user: a second start shows the first one's window.
	unlock, lerr := config.Lock(filepath.Join(dir, "desktop"))
	if lerr != nil {
		if b, err := os.ReadFile(filepath.Join(dir, urlFile)); err == nil && askShow(strings.TrimSpace(string(b))) == nil {
			log.Info("RemoHop is already running; showed its window")
			return cli.ExitOK
		}
		env.Errorf("app: RemoHop is already running (%v)", lerr)
		return cli.ExitError
	}
	defer unlock()

	// The smoke test takes screenshots; a notice about a newer release
	// would only make them differ from run to run.
	// REMOHOP_UPDATE_URL points the update check at another copy of the
	// version file (a mirror, or a test server).
	app, err := NewApp(Config{Signal: env.Globals.Signal, Logger: log, NoUpdateCheck: o.smoke != "", UpdateURL: os.Getenv("REMOHOP_UPDATE_URL")})
	if err != nil {
		env.Errorf("app: %v", err)
		return cli.ExitError
	}
	if o.lang != "" {
		app.mu.Lock()
		app.settings.Lang = normLang(o.lang)
		app.mu.Unlock()
	}
	srv, err := NewServer(app, log)
	if err != nil {
		env.Errorf("app: %v", err)
		return cli.ExitError
	}
	app.cfg.UIOrigin = srv.Origin()
	go func() {
		if err := srv.Serve(); err != nil {
			log.Error("the UI server stopped", "err", err)
		}
	}()
	defer srv.Close()
	if err := app.Start(); err != nil {
		env.Errorf("app: %v", err)
		return cli.ExitError
	}
	defer app.Stop()
	urlPath := filepath.Join(dir, urlFile)
	if err := config.WriteFileAtomic(urlPath, []byte(srv.URL()), 0o600); err == nil {
		defer os.Remove(urlPath)
	}

	if o.smoke != "" {
		return runSmoke(ctx, env, app, srv, o)
	}
	if o.noOpen {
		fmt.Fprintf(env.Stdout, "RemoHop: %s\n", srv.URL())
		fmt.Fprintln(env.Stdout, "the token in the address is the login to this page; do not share it")
		<-ctx.Done()
		return cli.ExitOK
	}
	if !o.browser {
		err := runNative(ctx, app, srv, o, log)
		if err == nil {
			return cli.ExitOK
		}
		log.Info("no native window; using a browser", "reason", err)
	}
	if err := runBrowser(ctx, app, srv, o, log); err != nil {
		env.Errorf("app: could not open a window: %v (the page is at the address in %s)", err, urlPath)
		return cli.ExitError
	}
	return cli.ExitOK
}

// askShow asks the app running at target (its page address) to show its
// window: hello for the CSRF token, then show.
func askShow(target string) error {
	if !strings.HasPrefix(target, "http://127.0.0.1:") {
		return fmt.Errorf("not a local address")
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Post(target+"api/hello", "application/json", strings.NewReader("{}"))
	if err != nil {
		return err
	}
	var h struct {
		CSRF string `json:"csrf"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&h)
	resp.Body.Close()
	if err != nil || h.CSRF == "" {
		return fmt.Errorf("no answer")
	}
	req, _ := http.NewRequest("POST", target+"api/show", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(localui.HeaderCSRF, h.CSRF)
	resp, err = c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("show: %s", resp.Status)
	}
	return nil
}

// uiLang is the language for the shell's own words (tray menu, banner
// window title): the app's setting, else the system's.
func uiLang(a *App) string {
	a.mu.Lock()
	l := a.settings.Lang
	a.mu.Unlock()
	if l == "" {
		l = quick.SystemLang()
	}
	if l != "zh" {
		l = "en"
	}
	return l
}

var shellText = map[string]map[string]string{
	"en": {"show": "Show RemoHop", "quit": "Quit RemoHop", "tip": "RemoHop", "controlled": "RemoHop: %s is connected to this computer", "banner": "RemoHop: being controlled"},
	"zh": {"show": "显示 RemoHop", "quit": "退出 RemoHop", "tip": "RemoHop", "controlled": "RemoHop：%s 正连接到这台电脑", "banner": "RemoHop：正在被控制"},
}

func st(a *App, key string) string { return shellText[uiLang(a)][key] }
