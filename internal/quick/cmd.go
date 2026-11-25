package quick

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
)

const usage = `Usage: remohop quick [--no-open] [--browser] [--lang zh|en]

Quick support: share this computer with someone who is helping you, without
a terminal. A window shows this machine's 9-digit ID and a temporary
password to pass on. Every connection has to be allowed in that window (or
in the system dialog that pops up) before the other side sees anything;
without an answer it is refused after a minute. The window lists who is
connected, with a Disconnect button each, and "Stop sharing" ends
everything. Closing the window stops sharing as well.

The RemoHop.exe and RemoHop.app downloads are this command behind a
double-click.

Whatever ` + "`remohop config`" + ` says, a quick-support host asks about every login
(pairings and authorised keys too), accepts only the temporary password of
this run, mints no pairings, and offers the screen plus what is ticked in
the window: mouse and keyboard, files and clipboard (ticked at start),
terminal and network access (not ticked). Features switched off with
` + "`remohop config feature`" + ` stay off.

The window is a page served on 127.0.0.1 with an unguessable token in its
address; it cannot be reached from another machine. On Windows it opens in
an app window of Microsoft Edge, elsewhere in the default browser.

Flags:
  --no-open      print the page's address instead of opening a window, and
                 keep running until interrupted
  --browser      use the default browser even where an app window is possible
  --lang zh|en   language of the window and dialogs (default: the one last
                 picked in the window, else the system's)

The rendezvous server is the configured one (global --signal, REMOHOP_SIGNAL,
` + "`remohop config set signal`" + `). When none is configured the window asks once:
the official server (signal.remohop.com), the helper's server, or none
(connect by address); the choice is kept in the shared settings.
`

func init() {
	cli.Register(&cli.Command{
		Name:    "quick",
		Summary: "quick support: a window with this machine's ID and password, approve each connection",
		Usage:   usage,
		Run:     run,
	})
}

// urlFile is where a running quick-support process leaves the address of
// its page (mode 0600, in the config directory like the status file that
// holds the password), so that starting RemoHop a second time brings the
// window back instead of failing.
const urlFile = "quick.url"

// langFile keeps the language picked in the window (the page's own
// storage does not survive a restart: each start serves it on a new port),
// so the next start, and its native dialogs, use it too.
const langFile = "quick.lang"

// savedLang is the language saved in dir by an earlier window, or "".
func savedLang(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, langFile))
	if err != nil {
		return ""
	}
	return normLang(string(b))
}

// tolerant drops write errors: a program built for the Windows GUI
// subsystem has no usable stderr, and logging must not stop over it.
type tolerant struct{ w io.Writer }

func (t tolerant) Write(p []byte) (int, error) {
	t.w.Write(p)
	return len(p), nil
}

// openLog appends to quick.log in the config directory, starting it afresh
// when it has grown large.
func openLog(dir string) io.WriteCloser {
	path := filepath.Join(dir, "quick.log")
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
	fs := flag.NewFlagSet("quick", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	noOpen := fs.Bool("no-open", false, "")
	plain := fs.Bool("browser", false, "")
	langFlag := fs.String("lang", "", "")
	appBrowser := fs.String("app-browser", os.Getenv("REMOHOP_QUICK_APP_BROWSER"), "")
	if err := fs.Parse(args); err != nil {
		env.Errorf("quick: %v\n%s", err, usage)
		return cli.ExitUsage
	}
	if fs.NArg() != 0 {
		env.Errorf("quick: unexpected argument %q", fs.Arg(0))
		return cli.ExitUsage
	}
	lang := normLang(*langFlag)
	if *langFlag != "" && lang == "" {
		env.Errorf("quick: --lang is zh or en")
		return cli.ExitUsage
	}
	dir, dirErr := config.Dir()
	// --lang, else what the user picked in the window last time, else the
	// system's language.
	if lang == "" && dirErr == nil {
		lang = savedLang(dir)
	}
	if lang == "" {
		lang = SystemLang()
	}
	// The window's language switch changes it while running (see OnLang).
	var curLang atomic.Value
	curLang.Store(lang)
	uiLang := func() string { return curLang.Load().(string) }
	// fatal reports an error where the user will see it: a dialog unless
	// they are at a terminal that asked for no window.
	fatal := func(text string) int {
		env.Errorf("quick: %s", strings.ReplaceAll(text, "\n\n", " "))
		if !*noOpen {
			nativeError(uiLang(), text)
		}
		return cli.ExitError
	}

	if dirErr != nil {
		return fatal(msg(lang, "start_failed", dirErr))
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

	app := NewApp(AppConfig{Signal: env.Globals.Signal, Logger: log,
		Prompt: func(ctx context.Context, r Request) (bool, bool) { return nativeAsk(ctx, uiLang(), r, AskTimeout) }})
	app.Start()
	urlPath := filepath.Join(dir, urlFile)
	if st := app.State(); st.Phase == PhaseError && st.ErrorCode == ErrAlreadyRunning && !*noOpen {
		// If the host that is running is a quick-support one, show its
		// window again; that is what a second double-click means.
		if b, err := os.ReadFile(urlPath); err == nil && pageAlive(string(b)) {
			log.Info("quick support is already running; showing its window")
			// The page reports to the other process, so there is nothing
			// to wait for here: the first opener that starts is it.
			cleanup, _ := showWindow(string(b), msg(lang, "redirect_text"), openers(*appBrowser, *plain), time.Second,
				func() bool { return true }, log.Info)
			time.Sleep(5 * time.Second) // let the browser read the redirect file
			cleanup()
			return cli.ExitOK
		}
	}

	srv, err := NewServer(app, lang, log)
	if err != nil {
		app.Stop()
		return fatal(msg(lang, "start_failed", err))
	}
	srv.OnLang(func(l string) {
		curLang.Store(l)
		if err := config.WriteFileAtomic(filepath.Join(dir, langFile), []byte(l+"\n"), 0o600); err != nil {
			log.Warn("could not save the language", "err", err)
		}
	})
	go func() {
		if err := srv.Serve(); err != nil {
			log.Error("the UI server stopped", "err", err)
		}
	}()
	defer srv.Close()
	if st := app.State(); st.Phase != PhaseError {
		if err := config.WriteFileAtomic(urlPath, []byte(srv.URL()), 0o600); err == nil {
			defer os.Remove(urlPath)
		}
	}
	fmt.Fprintf(env.Stdout, "RemoHop quick support: %s\n", srv.URL())
	fmt.Fprintln(env.Stdout, "the token in the address is the login to this page; do not share it")

	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ended := make(chan string, 1)
	if !*noOpen {
		cleanup, err := showWindow(srv.URL(), msg(uiLang(), "redirect_text"), openers(*appBrowser, *plain), 20*time.Second,
			func() bool { return srv.Presence().Seen }, log.Info)
		defer cleanup()
		if err != nil {
			log.Error("could not open the window", "err", err)
			app.Stop()
			return fatal(msg(lang, "no_window"))
		}
		go func() {
			busy := func() bool {
				st := app.State()
				return len(st.Sessions) > 0 || len(st.Requests) > 0
			}
			if reason := defaultLife.watch(wctx, srv.Presence, srv.PresenceChanged(), busy); reason != "" {
				ended <- reason
			}
		}()
	}

	select {
	case <-ctx.Done():
		log.Info("interrupted; stopping")
	case reason := <-ended:
		log.Info("stopping: " + reason)
	case <-app.Done():
	}
	app.Stop()
	// Let open pages receive the final state before the server goes.
	time.Sleep(700 * time.Millisecond)
	return cli.ExitOK
}

// pageAlive reports whether a quick-support page answers at target, which
// must be a loopback address written by this program.
func pageAlive(target string) bool {
	if !strings.HasPrefix(target, "http://127.0.0.1:") {
		return false
	}
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(target)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
