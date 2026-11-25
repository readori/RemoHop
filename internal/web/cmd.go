package web

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/handoff"
)

const usage = `usage: remohop web [--listen ADDR] [--tls-cert FILE --tls-key FILE]
               [--password-file FILE] [--api-token-file FILE]
               [--public-url URL [--real-ip-header NAME]] [--no-open]
               [--target ID] [--no-saved]

Serve a browser client (remote desktop, terminal, files, clipboard, info)
from this machine.

A browser cannot hole-punch or speak the QUIC session, so this process is
the client: it speaks the normal end-to-end encrypted P2P session to the
remote host and serves the page. The browser talks to THIS GATEWAY, so the
gateway sees everything: the screen, every keystroke, files, the clipboard
and the host passwords you type into the page. Use only a gateway you
control, and never expose it beyond localhost without TLS and its own login.

Default: listen on 127.0.0.1 on a random port and open a URL that contains
an unguessable token. On loopback the token is the login; Host and Origin
headers are validated against DNS rebinding and cross-site WebSocket
hijacking, and the page uses no external assets.

Flags:
  --listen ADDR          address to listen on (default 127.0.0.1:0)
  --tls-cert FILE        PEM certificate (with --tls-key)
  --tls-key FILE         PEM private key
  --password-file FILE   the gateway password (at least 8 characters), read
                         from this file (mode 600); visitors must log in
  --api-token-file FILE  allow programs to authenticate with
                         "Authorization: Bearer <token>", the token read from
                         this file (mode 600); off unless given; needs
                         --password-file (see "Programs" below)
  --public-url URL       the https:// address browsers use when a TLS
                         reverse proxy or tunnel forwards to this gateway
                         (see "Behind Cloudflare Tunnel" below); needs a
                         loopback --listen and --password-file
  --real-ip-header NAME  with --public-url: the header in which the proxy
                         puts the visitor's address (CF-Connecting-IP for
                         Cloudflare), used to rate-limit logins per visitor.
                         Name only a header your proxy always sets; without
                         it, all visitors through the proxy share one limit
  --no-open              print the URL, do not start a browser
  --target ID            pre-fill the connect form with this host
  --no-saved             shared-gateway mode: hide saved peers and never use
                         saved pairings; users type the host's ID and password
                         every time (and cannot pair)

Self-hosting (a --listen address that is not loopback) REFUSES to start
without --tls-cert, --tls-key and --password-file. Login is rate limited
with lockout, the session cookie is HttpOnly, Secure and SameSite=Strict,
and every state-changing request carries a per-session CSRF token.

Behind Cloudflare Tunnel (or another TLS-terminating proxy): keep the
gateway on loopback with a fixed port and a password, and name the public
address so its Host and Origin headers are accepted:

  remohop web --listen 127.0.0.1:8470 --password-file ~/.config/remohop-web.pw \
          --public-url https://desk.example.com --real-ip-header CF-Connecting-IP \
          --no-open
  cloudflared tunnel ... --url http://127.0.0.1:8470

Put a Cloudflare Access policy in front of the hostname as well. With
--real-ip-header the login rate limiter keys on that header for requests
that arrive through the public address; name it only when the proxy sets it
(Cloudflare overwrites CF-Connecting-IP), since anyone can send it. A visitor
who has logged in from an address within 30 days is not held back by the
limit that strangers' failures use up, only by its own.

Programs: with --api-token-file, a program (not a browser) can skip the
login call and send the token on every request as "Authorization: Bearer
<token>", for example through a proxy that injects the header so the
program never sees the token. Such requests share one gateway session and
need no cookie, CSRF token or Origin, and failures count against the login
rate limit. Without --api-token-file the header is not accepted. A bearer
request that carries what browsers send (Origin, Sec-Fetch-*, cookies) is
held to the browser checks: same origin, and the CSRF token for changes; a
bearer WebSocket with an Origin must be same-origin. If a proxy injects the
token, scope that rule to program traffic only (a separate hostname or
path, or the program's own service-token identity), never to browsers.
Gateways that used the gateway password as the bearer before this flag
existed keep working with --api-token-file pointing at the password file;
a separate token is better.

Anyone who logs in to a gateway can use ITS saved pairings (the peers in its
config directory, REMOHOP_CONFIG_DIR). A shared gateway should run with an empty
config dir (or --no-saved) so users bring the remote host's ID and password.

In the page, a host password is sent once over the page's connection to the
gateway, used for that one login and not stored, unless "pair this gateway"
is ticked, which runs the pair flow and saves a token in the gateway's peers.

The host's own settings still apply: a feature the host switched off (shell,
file, clip, screen, input, info) shows the host's refusal in its tab.
`

func init() {
	cli.Register(&cli.Command{
		Name:    "web",
		Summary: "browser client gateway: desktop, terminal, files, clipboard",
		Usage:   usage,
		Run:     run,
	})
}

func run(ctx context.Context, env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("web", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	listen := fs.String("listen", "127.0.0.1:0", "")
	cert := fs.String("tls-cert", "", "")
	key := fs.String("tls-key", "", "")
	pwFile := fs.String("password-file", "", "")
	apiFile := fs.String("api-token-file", "", "")
	noOpen := fs.Bool("no-open", false, "")
	target := fs.String("target", "", "")
	noSaved := fs.Bool("no-saved", false, "")
	publicURL := fs.String("public-url", "", "")
	realIPHeader := fs.String("real-ip-header", "", "")
	if err := fs.Parse(args); err != nil {
		env.Errorf("web: %v\n%s", err, usage)
		return cli.ExitUsage
	}
	if fs.NArg() != 0 {
		env.Errorf("web: unexpected argument %q", fs.Arg(0))
		return cli.ExitUsage
	}
	srv, err := New(Config{Listen: *listen, TLSCert: *cert, TLSKey: *key, PasswordFile: *pwFile, APITokenFile: *apiFile,
		Target: cli.NormalizeTarget(*target), NoSaved: *noSaved, PublicURL: *publicURL, RealIPHeader: *realIPHeader})
	if err != nil {
		env.Errorf("web: %v", err)
		if errors.Is(err, errNoTLSPassword) {
			return cli.ExitUsage
		}
		return cli.ExitError
	}
	url := srv.URL()
	fmt.Fprintf(env.Stdout, "RemoHop web gateway: %s\n", url)
	if srv.PasswordMode() {
		fmt.Fprintln(env.Stdout, "log in with the gateway password from --password-file")
	} else {
		fmt.Fprintln(env.Stdout, "the token in the URL is the login; do not share it")
	}
	fmt.Fprintln(env.Stdout, "the gateway sees everything you do in the page; stop it with Ctrl-C")
	if *publicURL != "" && *realIPHeader == "" {
		env.Errorf("web: note: without --real-ip-header, all visitors through %s share one login rate limit (behind Cloudflare, add --real-ip-header CF-Connecting-IP)", *publicURL)
	}
	if !*noOpen {
		defer openBrowser(env, url)()
	}
	if err := srv.Serve(ctx); err != nil {
		env.Errorf("web: %v", err)
		return cli.ExitError
	}
	srv.Close()
	return cli.ExitOK
}

// openBrowser opens url in the default browser, best effort. The URL carries
// the access token and an opener's argument list is visible to every local
// user, so the browser is pointed at a private temporary file that only
// redirects. (The same technique as the desktop viewer's; see the report.)
func openBrowser(env *cli.Env, url string) (cleanup func()) {
	cleanup = func() {}
	fail := func(err error) { env.Errorf("web: opening browser: %v; open the URL above manually", err) }
	dir, err := handoff.Dir("")
	if err != nil {
		fail(err)
		return
	}
	handoff.Sweep(dir)
	path, remove, err := handoff.Write(dir, "web", handoff.Page(url, "Opening the RemoHop web client..."))
	if err != nil {
		fail(err)
		return
	}
	timer := time.AfterFunc(30*time.Second, remove)
	removeNow := func() { timer.Stop(); remove() }
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", path)
	default:
		cmd = exec.Command("xdg-open", path)
	}
	if err := cmd.Start(); err != nil {
		removeNow()
		if errors.Is(err, exec.ErrNotFound) {
			env.Errorf("web: could not open a browser; open the URL above manually")
		} else {
			fail(err)
		}
		return func() {}
	}
	go cmd.Wait()
	return removeNow
}
