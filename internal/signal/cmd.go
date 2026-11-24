package signal

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/account/ledger"
	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/config"
)

const signalUsage = `Usage: remohop signal [--listen ADDR] [--alt ADDR] [--data DIR] [--max-ids N]
                  [--relay [--relay-ports LO-HI] [--relay-kbps N] [--relay-total-kbps N]
                   [--relay-max N] [--relay-per-source N] [--relay-account-kbps N]
                   [--relay-for all|accounts] [--relay-deny FILE]
                   [--relay-monthly-gb N]] [--max-conns N]
                  [--ws-listen ADDR [--ws-path PATH] [--ws-real-ip-header NAME]
                   [--ws-tls-cert FILE --ws-tls-key FILE] [--ws-max-conns N]]
       remohop signal --print-firewall [same flags]
       remohop signal --show-key [--data DIR]
       remohop signal --status [--data DIR] [--relay-monthly-gb N]
                  [--accounts MAIL-OPTIONS WHO-OPTIONS [--max-accounts N]]

Run a rendezvous server. Hosts register with it to get their 9-digit ID and
clients ask it where a host is; the two then connect directly to each other.
The server never sees session traffic and cannot decrypt or impersonate a
session, so it needs no secrets beyond its own key and can run anywhere
that has a public UDP port.

Flags:
  --listen ADDR   UDP address to serve on (default ":21116")
  --alt ADDR      second UDP address for NAT-type detection (default: the
                  port after --listen); "off" disables it
  --data DIR      where the server key and the ID table live
                  (default: "signal" in the remohop config directory)
  --max-ids N     stop allocating new IDs beyond N hosts (default 1000000)
  --relay         also relay sessions whose two peers cannot connect
                  directly (two symmetric NATs, say). Off by default: it is
                  the only thing that costs this server real bandwidth.
                  Relayed traffic stays end-to-end encrypted between the
                  peers; the server passes ciphertext and cannot read or
                  join a session. Each relayed session uses its own UDP
                  port, which must be reachable too.
  --relay-ports LO-HI   take relay ports only from this range, e.g.
                  40000-40099, so the firewall needs to open just that
                  range; when all are in use, further relay requests are
                  refused ("full"). Without it each session gets a random
                  high port and the firewall must allow ALL of 1024-65535/udp.
  --relay-kbps N        bandwidth cap per relayed session (default 8000).
                        Traffic over it is paced (queued up to 100 ms, then
                        dropped), which QUIC follows smoothly.
  --relay-total-kbps N  bandwidth cap for all relayed sessions (default 40000)
  --relay-max N         concurrent relayed sessions (default 64, at most
                        the size of --relay-ports)
  --relay-per-source N  concurrent relayed sessions per address (an IPv6
                        address counts by its /64), counting both peers and
                        the real address of websocket clients (default 2;
                        0 for no limit). A session is freed as soon as the
                        client closes it.
  --relay-account-kbps N  per-session cap when either peer is a verified
                        member of an account on this server (needs
                        --accounts; default: --relay-kbps). --relay-kbps is
                        then the cap for anonymous sessions. A host proves
                        its key when it registers, a client by signing its
                        relay request; a claimed account counts for nothing.
  --relay-for all|accounts  "accounts" relays only sessions with such a peer
                        (default all)
  --relay-deny FILE     no relay for the addresses, networks (CIDR) and
                        identity keys listed, one per line, "#" comments;
                        listed keys also cannot register as hosts. Re-read
                        when the file changes (checked every 10 s) and on
                        SIGHUP.
  --relay-monthly-gb N  relay budget per calendar month (UTC), counted in
                        --data across restarts (0, the default: none). From
                        80% anonymous sessions get half their rate, from
                        100% they are refused, from 110% all new sessions
                        are. Logged at 50, 80 and 100%.
  --status        print this month's relay usage from --data and exit
  --max-conns N   open connections (hosts online plus clients asking) the
                  server accepts. Each costs about 70 KB; the default is
                  sized to the memory available (the cgroup limit, else the
                  RAM): about 6400 on a 1 GB machine, at most 50000
  --ws-listen ADDR   also serve the websocket bridge on this TCP address,
                  for peers in networks that let only HTTPS out (UDP
                  blocked, maybe only an HTTP proxy): they set
                  ` + "`remohop config set signal-ws wss://HOST/remohop/v1/udp`" + ` and fall
                  back to it by themselves. Plain HTTP, meant for a reverse
                  proxy or tunnel on this machine (127.0.0.1:8080, say);
                  add --ws-tls-cert/--ws-tls-key to serve HTTPS directly.
                  The bridge forwards datagrams only to this server's own
                  UDP ports (rendezvous, NAT detection, open relay
                  sessions), never elsewhere, and carries the same
                  end-to-end encrypted QUIC as UDP does. Bridged peers can
                  only meet through the relay, so it needs --relay.
  --ws-path PATH        URL path of the bridge (default /remohop/v1/udp)
  --ws-real-ip-header NAME  take the client address from this header set
                  by the proxy in front (CF-Connecting-IP behind a
                  Cloudflare Tunnel); without it every client counts as
                  the proxy's address for the per-address limits
  --ws-tls-cert FILE, --ws-tls-key FILE  serve wss:// directly
  --ws-max-conns N      open websocket connections (default 512; 32 per
                        address). Each is rate limited like a relayed
                        session and closed after 2 minutes of silence.
  --print-firewall  print the commands that open the ports these flags use,
                  for this OS (iptables on Ubuntu/Debian, firewall-cmd on
                  Oracle Linux/RHEL), and exit. Nothing is run.
  --show-key      print the server's public key and how to pin it in clients
                  or builds, and exit (creates the key on first use)

Accounts (off by default):
  --accounts      let people log in with an email address (` + "`remohop login`" + `) so
                  that each of their devices can list the others and see
                  which are online (` + "`remohop devices`" + `). An account never grants
                  access to a device: connecting still needs a password,
                  pairing or key that the host accepts. A host's owner may
                  choose to accept the account's other devices
                  (` + "`remohop config account-trust on`" + `); the host then checks a
                  membership list signed by the account's own devices, which
                  this server stores but cannot forge, so a compromised
                  server cannot add itself to an account.
  Login codes are mailed through SMTP:
  --smtp-host HOST         SMTP server
  --smtp-port N            its port (default 587; 465 implies --smtp-tls tls)
  --smtp-tls MODE          starttls (default; STARTTLS must succeed), tls
                           (TLS from the start), or none (only for a relay on
                           this machine)
  --smtp-user USER         user name for SMTP authentication
  --smtp-password-file F   file holding the SMTP password (or set
                           REMOHOP_SMTP_PASSWORD); never on the command line
  --mail-from ADDR         sender address of the mails
  Who may make an account (at least one is required, so that a public
  server is not a mail cannon aimed at arbitrary addresses):
  --account-domains LIST   comma-separated domains: any address in them
  --account-allow FILE     file of allowed addresses, one per line (an
                           invite list; "#" starts a comment)
  --account-open           any address at all
  --max-accounts N         stop creating accounts beyond N (default 100000)
  For development only:
  --accounts-dev-codes     write login codes to this server's log instead of
                           mailing them; no SMTP and no address list needed.
                           Anyone who reads the log can log in to any
                           account, so it is refused unless --listen is a
                           loopback address, or
  --dev-codes-on-public-address  is given as well.

  Codes have 8 digits, expire after 10 minutes and work once. Five wrong
  codes burn the code and lock the address for 15 minutes. Requests are
  rate limited per source address, per email address and in total. The
  server stores accounts in accounts.journal in --data, indexed by a hash
  of the address, with session tokens hashed; codes are never stored.

Both UDP ports must be reachable from the internet, through the machine's
own firewall AND the cloud provider's (an Oracle Cloud security list, an AWS
security group, ...): ` + "`remohop signal --print-firewall`" + ` prints the commands. On
start the server prints its public key and how clients pin it:
  remohop config set signal-pin KEY
or, for a distribution of remohop with this server built in:
  go build -ldflags "-X .../internal/dial.DefaultSignal=HOST -X .../internal/dial.DefaultSignalPin=KEY"

Small machines: idle, the server needs about 16 MB; each online host adds
about 70 KB, and the connection limit is sized to the memory available so a
small machine refuses connections rather than running out of memory. A
burst of 200 requests per second costs about a quarter of a fast CPU core.
Relaying costs about 1 % of a core per 10 Mbit/s, so the default total of
40 Mbit/s fits a 1/8-CPU VM; mind the provider's monthly traffic allowance. To run it as a hardened system service:
  sudo remohop service install --role signal --relay --relay-ports 40000-40099

Examples:
  remohop signal
  remohop signal --listen :443 --alt :444 --data /var/lib/remohop
  remohop -v signal                   also log every introduction
  remohop signal --accounts --smtp-host smtp.example.com --smtp-user remohop \
      --smtp-password-file /etc/remohop/smtp-pass --mail-from remohop@example.com \
      --account-domains example.com
  remohop signal --listen 127.0.0.1:21116 --accounts --accounts-dev-codes

On the machines that use it:
  remohop config set signal rendezvous.example.net

A free public server (a relay anybody may use, within limits): see
docs/public-server.md. On an Oracle Always Free micro VM (10 TB/month):
  remohop signal --relay --relay-ports 40000-40099 --relay-kbps 2000 \
      --relay-per-source 2 --relay-monthly-gb 8000 \
      --ws-listen 127.0.0.1:8080 --ws-real-ip-header CF-Connecting-IP
  (with --accounts ... add --relay-account-kbps 8000). An hourly summary
  line counts sessions, bytes and refusals, without addresses.

Behind a Cloudflare Tunnel (only the websocket part goes through it; the
UDP ports stay open as usual):
  remohop signal --relay --relay-ports 40000-40099 \
      --ws-listen 127.0.0.1:8080 --ws-real-ip-header CF-Connecting-IP
  and in cloudflared's config.yml:
    ingress:
      - hostname: relay.example.com
        service: http://127.0.0.1:8080
      - service: http_status:404
  then on the machines:
    remohop config set signal-ws wss://relay.example.com/remohop/v1/udp
  With Cloudflare Access in front, give machines a service token:
    remohop config set signal-ws-access-id ID.access
    remohop config set signal-ws-access-secret-file /path/to/secret
  (or REMOHOP_SIGNAL_WS_ACCESS_SECRET; never on the command line).
`

func init() {
	cli.Register(&cli.Command{
		Name:    "signal",
		Summary: "run a rendezvous server",
		Usage:   signalUsage,
		Run:     runSignal,
	})
}

func runSignal(ctx context.Context, env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("signal", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() { fmt.Fprint(env.Stderr, signalUsage) }
	listen := fs.String("listen", ":"+strconv.Itoa(DefaultPort), "")
	alt := fs.String("alt", "", "")
	data := fs.String("data", "", "")
	maxIDs := fs.Int("max-ids", 0, "")
	relay := fs.Bool("relay", false, "")
	relayKbps := fs.Int("relay-kbps", 8000, "")
	relayTotal := fs.Int("relay-total-kbps", 40000, "")
	relayMax := fs.Int("relay-max", 64, "")
	relayPorts := fs.String("relay-ports", "", "")
	maxConns := fs.Int("max-conns", 0, "")
	printFW := fs.Bool("print-firewall", false, "")
	showKeyOnly := fs.Bool("show-key", false, "")
	statusOnly := fs.Bool("status", false, "")
	relayPerSource := fs.Int("relay-per-source", 2, "")
	relayAccountKbps := fs.Int("relay-account-kbps", 0, "")
	relayFor := fs.String("relay-for", "all", "")
	relayDeny := fs.String("relay-deny", "", "")
	relayMonthlyGB := fs.Int64("relay-monthly-gb", 0, "")
	wsListen := fs.String("ws-listen", "", "")
	wsPath := fs.String("ws-path", "", "")
	wsRealIP := fs.String("ws-real-ip-header", "", "")
	wsCert := fs.String("ws-tls-cert", "", "")
	wsKey := fs.String("ws-tls-key", "", "")
	wsMax := fs.Int("ws-max-conns", 0, "")
	accountsOn := fs.Bool("accounts", false, "")
	smtpHost := fs.String("smtp-host", "", "")
	smtpPort := fs.Int("smtp-port", 0, "")
	smtpTLS := fs.String("smtp-tls", "", "")
	smtpUser := fs.String("smtp-user", "", "")
	smtpPassFile := fs.String("smtp-password-file", "", "")
	mailFrom := fs.String("mail-from", "", "")
	accountDomains := fs.String("account-domains", "", "")
	accountAllow := fs.String("account-allow", "", "")
	accountOpen := fs.Bool("account-open", false, "")
	maxAccounts := fs.Int("max-accounts", 0, "")
	devCodes := fs.Bool("accounts-dev-codes", false, "")
	devCodesPublic := fs.Bool("dev-codes-on-public-address", false, "")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() != 0 {
		env.Errorf("signal: unexpected argument %q", fs.Arg(0))
		return cli.ExitUsage
	}
	if *maxIDs < 0 {
		env.Errorf("signal: --max-ids must be positive")
		return cli.ExitUsage
	}

	var ports PortRange
	if *relayPorts != "" {
		var err error
		if ports, err = ParsePortRange(*relayPorts); err != nil {
			env.Errorf("signal: --relay-ports: %v", err)
			return cli.ExitUsage
		}
		if !*relay && !*printFW {
			env.Errorf("signal: --relay-ports needs --relay")
			return cli.ExitUsage
		}
	}
	if *maxConns < 0 {
		env.Errorf("signal: --max-conns must be positive")
		return cli.ExitUsage
	}
	if err := checkWSFlags(*wsListen, *wsPath, *wsCert, *wsKey, *wsRealIP, *wsMax); err != nil {
		env.Errorf("signal: %v", err)
		return cli.ExitUsage
	}
	if *printFW {
		plan, err := firewallPlanFor(*listen, *alt, *relay, ports, *wsListen)
		if err != nil {
			env.Errorf("signal: %v", err)
			return cli.ExitUsage
		}
		printFirewall(env.Stdout, plan, osFamily("/etc/os-release"))
		return cli.ExitOK
	}

	dir := *data
	if dir == "" {
		var err error
		if dir, err = config.SubDir("signal"); err != nil {
			env.Errorf("signal: %v", err)
			return cli.ExitError
		}
	}
	if *statusOnly {
		if err := printRelayStatus(env.Stdout, dir, *relayMonthlyGB*1e9, time.Now()); err != nil {
			env.Errorf("signal: %v", err)
			return cli.ExitError
		}
		return cli.ExitOK
	}
	if *showKeyOnly {
		_, p, _ := net.SplitHostPort(*listen)
		port, _ := strconv.Atoi(p)
		if err := showKey(env.Stdout, dir, port); err != nil {
			env.Errorf("signal: %v", err)
			return cli.ExitError
		}
		return cli.ExitOK
	}
	// --data may name a directory that does not exist yet (a manual
	// deployment; under systemd, StateDirectory creates it).
	if err := os.MkdirAll(dir, 0o700); err != nil {
		env.Errorf("signal: %v", err)
		return cli.ExitError
	}
	level := slog.LevelInfo
	if env.Globals.Verbose {
		level = slog.LevelDebug
	}
	if !*relay && (*relayDeny != "" || *relayMonthlyGB != 0 || *relayFor != "all" || *relayAccountKbps != 0) {
		env.Errorf("signal: the --relay-* options need --relay")
		return cli.ExitUsage
	}
	var relayCfg *RelayConfig
	if *relay {
		if *relayKbps <= 0 || *relayTotal <= 0 || *relayMax <= 0 {
			env.Errorf("signal: the relay limits must be positive")
			return cli.ExitUsage
		}
		if err := checkRelayPolicyFlags(*relayPerSource, *relayAccountKbps, *relayFor, *relayMonthlyGB, *accountsOn); err != nil {
			env.Errorf("signal: %v", err)
			return cli.ExitUsage
		}
		relayCfg = &RelayConfig{Rate: *relayKbps * 125, TotalRate: *relayTotal * 125, MaxSessions: *relayMax,
			PerSource: *relayPerSource, AccountRate: *relayAccountKbps * 125, ForAccounts: *relayFor == "accounts",
			DenyFile: *relayDeny, MonthlyBytes: *relayMonthlyGB * 1e9}
		if ports.Lo != 0 {
			host, _, _ := net.SplitHostPort(*listen)
			relayCfg.Listen = PortRangeListener(host, ports)
			relayCfg.MaxSessions = min(relayCfg.MaxSessions, ports.Size())
		}
	}
	logger := slog.New(slog.NewTextHandler(env.Stderr, &slog.HandlerOptions{Level: level}))
	var accountsCfg *AccountsConfig
	if *accountsOn {
		var err error
		accountsCfg, err = accountsConfig(accountFlags{
			smtpHost: *smtpHost, smtpPort: *smtpPort, smtpTLS: *smtpTLS, smtpUser: *smtpUser, smtpPassFile: *smtpPassFile,
			mailFrom: *mailFrom, domains: *accountDomains, allowFile: *accountAllow, open: *accountOpen,
			maxAccounts: *maxAccounts, devCodes: *devCodes, devCodesPublic: *devCodesPublic, listen: *listen,
		}, logger)
		if err != nil {
			env.Errorf("signal: %v", err)
			return cli.ExitUsage
		}
	} else if *smtpHost != "" || *devCodes || *accountDomains != "" || *accountAllow != "" || *accountOpen {
		env.Errorf("signal: account options need --accounts")
		return cli.ExitUsage
	}
	if *maxConns == 0 {
		// Size the connection limit, and the garbage collector's target,
		// to the memory this process may use, so that a small machine
		// refuses connections instead of running out of memory.
		budget := memoryBudget("/sys/fs/cgroup", "/proc/self/cgroup", "/proc/meminfo")
		*maxConns = defaultMaxConns(budget)
		if budget > 0 && os.Getenv("GOMEMLIMIT") == "" {
			debug.SetMemoryLimit(budget * 8 / 10)
		}
		if *maxConns > 0 {
			logger.Info("connection limit sized to memory", "max_conns", *maxConns, "memory_mb", budget>>20)
		}
	}
	srv, err := NewServer(Config{
		Accounts: accountsCfg,
		Relay:    relayCfg,
		DataDir:  dir,
		Logger:   logger,
		Limits:   Limits{MaxIDs: *maxIDs, MaxConns: *maxConns},
	})
	if err != nil {
		env.Errorf("signal: %v", err)
		return cli.ExitError
	}
	defer srv.Close()

	mainConn, err := net.ListenPacket("udp", *listen)
	if err != nil {
		env.Errorf("signal: %v", err)
		return cli.ExitError
	}
	var altConn net.PacketConn
	if *alt != "off" {
		altAddr := *alt
		if altAddr == "" {
			host, _, _ := net.SplitHostPort(*listen)
			port := mainConn.LocalAddr().(*net.UDPAddr).Port + 1
			altAddr = net.JoinHostPort(host, strconv.Itoa(port))
		}
		if altConn, err = net.ListenPacket("udp", altAddr); err != nil {
			mainConn.Close()
			env.Errorf("signal: NAT-detection port: %v (choose another with --alt, or disable with --alt off)", err)
			return cli.ExitError
		}
	}

	fmt.Fprintf(env.Stdout, "rendezvous server listening on %s", mainConn.LocalAddr())
	if altConn != nil {
		fmt.Fprintf(env.Stdout, " (NAT detection on %s)", altConn.LocalAddr())
	}
	fmt.Fprintln(env.Stdout)
	printPin(env.Stdout, auth.EncodeKey(srv.PublicKey()), mainConn.LocalAddr().(*net.UDPAddr).Port)
	if accountsCfg != nil {
		if *devCodes {
			fmt.Fprintln(env.Stdout, "accounts: on, DEVELOPMENT MODE: login codes are written to the log, not mailed")
		} else {
			fmt.Fprintln(env.Stdout, "accounts: on")
		}
	}
	if relayCfg != nil {
		fmt.Fprintf(env.Stdout, "relay: on (up to %d sessions, %d kbit/s each, %d kbit/s in total", relayCfg.MaxSessions, *relayKbps, *relayTotal)
		fmt.Fprint(env.Stdout, relayPolicyLine(relayCfg, srv))
		if ports.Lo != 0 {
			fmt.Fprintf(env.Stdout, ", ports %s)\n", ports)
		} else {
			fmt.Fprintln(env.Stdout, ")")
			logger.Warn("relay on without --relay-ports: each relayed session gets a random high UDP port, so the firewall must allow all of 1024-65535/udp; use --relay-ports LO-HI to confine them")
		}
	}

	if *wsListen != "" {
		stopWS, err := startWS(ctx, srv, wsOptions{listen: *wsListen, path: *wsPath, cert: *wsCert, key: *wsKey,
			realIP: *wsRealIP, maxConns: *wsMax, udpListen: mainConn.LocalAddr()}, env.Stdout, logger)
		if err != nil {
			mainConn.Close()
			if altConn != nil {
				altConn.Close()
			}
			env.Errorf("signal: %v", err)
			return cli.ExitError
		}
		defer stopWS()
		if relayCfg == nil {
			logger.Warn("websocket bridge without --relay: bridged peers can register and look up hosts, but cannot connect to anyone")
		}
	}

	if relayCfg != nil && relayCfg.DenyFile != "" {
		stopHUP := onHangup(func() {
			if err := srv.ReloadRelayDeny(); err != nil {
				logger.Error("relay deny list not reloaded; the previous one stays in force", "err", err)
			}
		})
		defer stopHUP()
	}

	if err := srv.Serve(ctx, mainConn, altConn); err != nil {
		env.Errorf("signal: %v", err)
		return cli.ExitError
	}
	return cli.ExitOK
}

type accountFlags struct {
	smtpHost, smtpTLS, smtpUser, smtpPassFile, mailFrom string
	smtpPort                                            int
	domains, allowFile                                  string
	open, devCodes, devCodesPublic                      bool
	maxAccounts                                         int
	listen                                              string
}

// accountsConfig turns `remohop signal` flags into an AccountsConfig.
func accountsConfig(f accountFlags, logger *slog.Logger) (*AccountsConfig, error) {
	cfg := &AccountsConfig{Open: f.open, MaxAccounts: f.maxAccounts}
	if f.maxAccounts < 0 {
		return nil, errors.New("--max-accounts must be positive")
	}
	for _, d := range strings.Split(f.domains, ",") {
		if d = strings.TrimSpace(d); d != "" {
			cfg.Domains = append(cfg.Domains, d)
		}
	}
	if f.allowFile != "" {
		b, err := os.ReadFile(f.allowFile)
		if err != nil {
			return nil, fmt.Errorf("--account-allow: %w", err)
		}
		for _, line := range strings.Split(string(b), "\n") {
			line, _, _ = strings.Cut(line, "#")
			if line = strings.TrimSpace(line); line == "" {
				continue
			}
			if _, err := ledger.NormalizeEmail(line); err != nil {
				return nil, fmt.Errorf("--account-allow %s: %w", f.allowFile, err)
			}
			cfg.Emails = append(cfg.Emails, line)
		}
	}
	if f.devCodes {
		if !f.devCodesPublic && !loopbackListen(f.listen) {
			return nil, fmt.Errorf("--accounts-dev-codes writes login codes to the log, so anyone who can read it can log in "+
				"to any account; it is refused while --listen %q is reachable from other machines. Use --listen 127.0.0.1:PORT, "+
				"or add --dev-codes-on-public-address if you really mean it", f.listen)
		}
		if f.smtpHost != "" {
			return nil, errors.New("--accounts-dev-codes and --smtp-host exclude each other")
		}
		if len(cfg.Domains) == 0 && len(cfg.Emails) == 0 {
			cfg.Open = true
		}
		cfg.Mailer = LogMailer{Log: logger}
		return cfg, nil
	}
	if f.smtpHost == "" {
		return nil, errors.New("--accounts needs --smtp-host to mail login codes (or --accounts-dev-codes for development)")
	}
	if f.mailFrom == "" {
		return nil, errors.New("--accounts needs --mail-from")
	}
	if _, err := ledger.NormalizeEmail(f.mailFrom); err != nil {
		return nil, fmt.Errorf("--mail-from: %w", err)
	}
	if !cfg.Open && len(cfg.Domains) == 0 && len(cfg.Emails) == 0 {
		return nil, errors.New("--accounts needs --account-domains, --account-allow or --account-open: " +
			"otherwise anybody could make this server mail any address")
	}
	m := &SMTPMailer{Host: f.smtpHost, Port: f.smtpPort, User: f.smtpUser, From: f.mailFrom, Security: f.smtpTLS}
	if m.Port == 0 {
		m.Port = 587
	}
	switch m.Security {
	case "":
		m.Security = "starttls"
		if m.Port == 465 {
			m.Security = "tls"
		}
	case "starttls", "tls", "none":
	default:
		return nil, fmt.Errorf("--smtp-tls %q: use starttls, tls or none", m.Security)
	}
	if m.Port <= 0 || m.Port > 65535 {
		return nil, errors.New("--smtp-port out of range")
	}
	switch {
	case f.smtpPassFile != "":
		b, err := os.ReadFile(f.smtpPassFile)
		if err != nil {
			return nil, fmt.Errorf("--smtp-password-file: %w", err)
		}
		m.Password = strings.TrimRight(string(b), "\r\n")
	default:
		m.Password = os.Getenv("REMOHOP_SMTP_PASSWORD")
	}
	if m.User != "" && m.Password == "" {
		return nil, errors.New("--smtp-user needs a password: --smtp-password-file FILE or REMOHOP_SMTP_PASSWORD")
	}
	cfg.Mailer = m
	return cfg, nil
}

// loopbackListen reports whether a --listen address only accepts packets
// from this machine.
func loopbackListen(listen string) bool {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback()
}
