package dial

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/signal"
)

// Connection outcomes (see signal.Outcome).
//
// After a dial by ID the client tells the rendezvous server how it went:
// which kind of path the session runs on, or that none was found, how long
// that took from the connect request, and the two NAT types of the
// introduction. A relayed session that later moves to a direct path
// (upgrade.go) reports that as well. A report names no peer, ID or
// address, and travels on the connection to the server the dial already
// has: the server keeps only counts. Reports are sent in the background
// and never hold up a session or an error; a server that does not take
// them is not asked again while the process runs. The "outcomes" setting
// and REMOHOP_OUTCOMES=0 turn them off.

// SettingOutcomes is the config key that turns connection outcome reports
// off. REMOHOP_OUTCOMES=0 overrides it to off.
const SettingOutcomes = "outcomes"

func init() {
	config.RegisterSetting(config.Setting{Key: SettingOutcomes, Default: "on", Validate: config.ValidateOnOff,
		Help: "after each connection by ID, tell the rendezvous server which kind of path it took, how long and the NAT types (no ID or address), for its aggregate counts"})
}

// OutcomesAllowed reports whether a client keeping its config in dir
// reports connection outcomes: unless REMOHOP_OUTCOMES says off, or the
// outcomes setting does.
func OutcomesAllowed(dir string) bool {
	switch strings.ToLower(os.Getenv("REMOHOP_OUTCOMES")) {
	case "0", "off", "no", "false":
		return false
	}
	var values map[string]string
	if dir == "" {
		values, _ = config.All()
	} else {
		values, _ = config.AllIn(dir)
	}
	return config.Bool(values[SettingOutcomes], true)
}

// outcomeTimeout bounds one report.
const outcomeTimeout = 2 * time.Second

// noOutcomes holds the keys of the servers that answered that they take no
// reports (signal.ErrNoOutcomes); they are not asked again.
var noOutcomes sync.Map

// sendOutcome sends one report. Tests replace it; a dialer takes it when
// it is made (so a test that changes it does not race with reports of an
// earlier one still on their way).
var sendOutcome = func(ctx context.Context, sc *signal.Client, o signal.Outcome) error {
	return sc.ReportOutcome(ctx, o)
}

// reportsTo reports whether outcomes go to the server sc is connected to.
func (d *dialer) reportsTo(sc *signal.Client) bool {
	if !OutcomesAllowed(d.dir) {
		return false
	}
	_, refused := noOutcomes.Load(string(sc.ServerKey()))
	return !refused
}

// report sends o on sc in the background and closes sc once it is done.
// Until then the dial's socket is not closed (closeSocket).
func (d *dialer) report(sc *signal.Client, o signal.Outcome) {
	done := make(chan struct{})
	d.reportsMu.Lock()
	d.reports = append(d.reports, done)
	d.reportsMu.Unlock()
	send, log := d.send, d.log
	go func() {
		defer close(done)
		defer sc.Close()
		ctx, cancel := context.WithTimeout(context.Background(), outcomeTimeout)
		defer cancel()
		err := send(ctx, sc, o)
		switch {
		case errors.Is(err, signal.ErrNoOutcomes):
			noOutcomes.Store(string(sc.ServerKey()), true)
			log.Debug("the rendezvous server does not take connection outcomes; not reporting to it again")
		case err != nil:
			log.Debug("connection outcome not reported", "path", o.Path, "err", err)
		default:
			log.Debug("connection outcome reported", "path", o.Path, "ms", o.MS)
		}
	}()
}

// closeSocket closes a socket the dial gives up, at once unless a report
// is still on its way over it: then as soon as that is done.
func (d *dialer) closeSocket(sock *nat.Socket) {
	d.reportsMu.Lock()
	pending := slices.Clone(d.reports)
	d.reportsMu.Unlock()
	if len(pending) == 0 {
		sock.Close()
		return
	}
	go func() {
		for _, ch := range pending {
			<-ch
		}
		sock.Close()
	}()
}

// finishSignal ends establish's use of the rendezvous connection: with a
// report of how the attempt ended when there is one to make, else simply
// by closing it. intro is the attempt's introduction (nil before there is
// one: nothing to report then).
func (d *dialer) finishSignal(ctx context.Context, sc *signal.Client, intro *signal.Introduction, rt route, err error) {
	o, ok := d.outcomeOf(ctx, intro, rt, err)
	if !ok || !d.reportsTo(sc) {
		sc.Close()
		return
	}
	d.report(sc, o)
}

// outcomeOf is the report on an attempt that ended with rt and err, if
// there is one to make: not before an introduction, not when the dial was
// cancelled, and not for a failed punch that is about to be tried again.
func (d *dialer) outcomeOf(ctx context.Context, intro *signal.Introduction, rt route, err error) (signal.Outcome, bool) {
	if intro == nil || ctx.Err() != nil || d.connectStart.IsZero() {
		return signal.Outcome{}, false
	}
	if _, retry := errors.AsType[*errRetryPunch](err); retry {
		return signal.Outcome{}, false
	}
	o := signal.Outcome{Path: rt.outcome, MS: time.Since(d.connectStart).Milliseconds(),
		NAT: string(d.natType), PeerNAT: string(intro.Peer.NAT)}
	if err != nil {
		o.Path = signal.PathFailed
	}
	if o.NAT == "" {
		o.NAT = string(nat.TypeUnknown)
	}
	o.MS = min(o.MS, signal.MaxOutcomeMS)
	return o, o.Valid()
}

// outcomePath names the kind of path a punched session runs on, for its
// outcome report: classify's Path, refined by what the punch knows (the
// winner was one of the host's predicted ports) and what the host
// advertised (a port its router forwards).
func outcomePath(path Path, win nat.Punched, host signal.Peer) string {
	switch {
	case path == PathLAN || path == PathTailnet:
		// A tailnet address is a private overlay address: as good as a LAN.
		return signal.PathLAN
	case path == PathRelayed:
		return signal.PathRelayed
	case mappedCandidate(win.Remote, host):
		return signal.PathMapped
	case win.Predicted:
		return signal.PathPredicted
	case path == PathDirect, win.Remote == host.Reflexive && host.NAT == nat.TypeNone:
		// One of the host's own public addresses, or the one the server
		// saw it at with no NAT in between (hostd's pathOf calls the
		// latter direct too).
		return signal.PathDirect
	}
	return signal.PathPunched
}

// mappedCandidate reports whether remote is the port the host's router
// forwards to it (nat.Socket.Mapped, internal/portmap), as the host
// advertised it. The rendezvous server lets a self-reported public IPv4
// candidate through only on the address it saw the host at, so one listed
// there is the host's own address (a host without NAT, whose candidate is
// its socket's port, the one the server saw), or the forwarded port: on a
// port other than the one the server saw, or behind a NAT the host
// measured.
func mappedCandidate(remote netip.AddrPort, host signal.Peer) bool {
	if !remote.Addr().Is4() || remote.Addr() != host.Reflexive.Addr() || !slices.Contains(host.Local, remote) {
		return false
	}
	return remote.Port() != host.Reflexive.Port() || host.NAT == nat.TypeCone || host.NAT == nat.TypeSymmetric
}
