package dial

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/quic-go/quic-go"

	"github.com/readori/RemoHop/internal/nat"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/signal"
)

// Moving a relayed session to a direct path.
//
// A session that ended up on the relay (punching failed, or a relay hint
// sent it there at once) would otherwise stay there for its whole life:
// slower, capped by the operator's budget, and at the operator's cost. Many
// of the reasons a punch fails pass: a NAT that only opens after a while, a
// race lost to timing, a network that changed under one side. So while the
// session lasts, the client keeps trying in the background, and when a
// direct path works it moves the session there without interrupting it.
//
// Each attempt is what the dial did: a fresh introduction through the
// rendezvous server (its existing "connect" request, which a host answers
// by punching as it always does) and a punch from the session's own socket.
// When a path answers, the session's QUIC connection probes it and switches
// to it: QUIC connection migration (see nat.PathConn). Every stream, the
// login and the host's authorisation carry over; nothing is asked of the
// host's owner, and the host needs nothing new (quic-go servers accept a
// validated migration by default). The new path is validated inside the
// session's own encryption, so only the host the session authenticated can
// make it work; an address that answers the punch but is not that host is
// simply never switched to.
//
// The relay is released a moment after the switch, when the host has had
// time to follow, and the session's Info says how it travels now.

// upgradeSchedule is the wait before each of the first attempts (counted
// from the previous one, the first from when the session was established),
// upgradeEvery the wait between the later ones. Variables for tests.
var (
	upgradeSchedule = []time.Duration{5 * time.Second, 20 * time.Second, 60 * time.Second}
	upgradeEvery    = 3 * time.Minute
	// netCheckEvery is how often the machine's addresses are compared
	// with the last ones; a change triggers an attempt at once.
	netCheckEvery = 5 * time.Second
	// probeTime bounds the validation of a new path: a PATH_CHALLENGE
	// goes out at once and is repeated with backoff (0.2, 0.4, 0.8, 1.6 s).
	probeTime = 4 * time.Second
	// relayDrain is how long the relay is kept after the switch, for the
	// packets the host sends through it until it has followed.
	relayDrain = 3 * time.Second
)

// onUpgradeAttempt, when set (by tests), is called before each attempt
// (n counts from 1); testHookUpgrader, when set, gets each upgrader made
// for a relayed session, whether or not it runs.
var (
	onUpgradeAttempt func(n int)
	testHookUpgrader func(u *upgrader)
)

// upgradeTiming is the schedule and limits one upgrader keeps to, taken
// from the variables above when it is made (so a test that changes them
// does not race with upgraders still winding down).
type upgradeTiming struct {
	schedule                      []time.Duration
	every, netCheck, probe, drain time.Duration
	onAttempt                     func(n int)
}

func currentTiming() upgradeTiming {
	return upgradeTiming{schedule: upgradeSchedule, every: upgradeEvery, netCheck: netCheckEvery,
		probe: probeTime, drain: relayDrain, onAttempt: onUpgradeAttempt}
}

// upgradesAllowed reports whether relayed sessions may move to a direct
// path: not with REMOHOP_RELAY_UPGRADE set to 0, off, no or false.
func upgradesAllowed() bool {
	switch strings.ToLower(os.Getenv("REMOHOP_RELAY_UPGRADE")) {
	case "0", "off", "no", "false":
		return false
	}
	return true
}

// upgrader tries to move one relayed session to a direct path.
type upgrader struct {
	d       *dialer
	t       *target
	sock    *nat.Socket
	q       *quic.Conn
	conn    *peer.Conn
	release func() // releases the relay session (once)
	start   time.Time
	timing  upgradeTiming

	// pc and tr are the path the connection moves to, made on first use
	// and kept (see nat.PathConn) until the connection has ended.
	pc *nat.PathConn
	tr *quic.Transport

	attempts int
}

// errNoMigration is an attempt's error that makes further ones pointless.
type errNoMigration struct{ err error }

func (e *errNoMigration) Error() string { return e.err.Error() }
func (e *errNoMigration) Unwrap() error { return e.err }

// run makes attempts on the schedule, and at once when the machine's
// network changes, until one succeeds or the session ends.
func (u *upgrader) run() {
	ctx := u.conn.Context()
	waitFor := func(i int) time.Duration {
		if i < len(u.timing.schedule) {
			return u.timing.schedule[i]
		}
		return u.timing.every
	}
	next := 0
	timer := time.NewTimer(waitFor(next))
	defer timer.Stop()
	netTick := time.NewTicker(u.timing.netCheck)
	defer netTick.Stop()
	network := netPrint(u.sock.Locals())
	for {
		scheduled := false
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			scheduled = true
		case <-netTick.C:
			n := netPrint(u.sock.Locals())
			if n == network {
				continue
			}
			network = n
			u.d.log.Debug("the network changed; looking for a direct path for the relayed session now", "target", u.t.canonical)
		}
		done, err := u.attempt(ctx)
		if done {
			return
		}
		if _, fatal := errors.AsType[*errNoMigration](err); fatal {
			u.d.log.Debug("the relayed session cannot move to a direct path; no more attempts", "target", u.t.canonical, "err", err)
			return
		}
		if err != nil && ctx.Err() == nil {
			u.d.log.Debug("no direct path for the relayed session yet", "target", u.t.canonical, "attempt", u.attempts, "err", err)
		}
		if scheduled {
			next++
			timer.Reset(waitFor(next))
		}
	}
}

// attempt gets a fresh introduction, punches, and moves the session to the
// first path that answers and validates. done reports success.
func (u *upgrader) attempt(ctx context.Context) (done bool, err error) {
	u.attempts++
	if u.timing.onAttempt != nil {
		u.timing.onAttempt(u.attempts)
	}
	d, t := u.d, u.t
	sctx, cancel := context.WithTimeout(ctx, signalTimeout)
	defer cancel()
	sc, err := signal.Dial(sctx, u.sock, d.signalAddr, d.sopts)
	if err != nil {
		return false, err
	}
	// The server's part is done with the introduction. The connection is
	// kept only for reporting a move (outcome.go), if reports go to it.
	report, reported := d.reportsTo(sc), false
	defer func() {
		if !reported {
			sc.Close()
		}
	}()
	natType := d.natType
	if natType == "" || natType == nat.TypeUnknown {
		natType = sc.NATType()
	}
	intro, err := sc.Connect(sctx, t.id, u.sock.Candidates(), natType)
	if err != nil {
		return false, err
	}
	if !report {
		sc.Close()
	}
	if sc.Bridged() || intro.Peer.Bridged {
		return false, errors.New("a websocket bridge is in the way")
	}
	_, ownPred := u.sock.Prediction()
	predicted := nat.UsesPrediction(natType, intro.Peer.NAT, ownPred, intro.Peer.Prediction.Valid())
	punchTime := nat.PunchTime(natType, intro.Peer.NAT, predicted)
	if d.opts.PunchTime != 0 {
		punchTime = d.opts.PunchTime
	}
	sess, err := u.sock.PunchWith(nat.PunchConfig{Nonce: intro.Nonce, Role: nat.RoleClient, Remote: intro.Peer.Candidates(),
		Lifetime: punchTime + u.timing.probe, Predicted: intro.Peer.Prediction})
	if err != nil {
		return false, err
	}
	defer sess.Close()
	deadline := time.NewTimer(punchTime)
	defer deadline.Stop()
	tried := 0
	var lastErr error
	for {
		select {
		case addr := <-sess.Paths():
			if tried == maxPathTries {
				continue
			}
			tried++
			d.log.Debug("a direct path answered; moving the relayed session to it", "target", t.canonical, "remote", addr)
			if err := u.migrate(ctx, addr); err != nil {
				if _, fatal := errors.AsType[*errNoMigration](err); fatal {
					return false, err
				}
				lastErr = fmt.Errorf("path to %s: %w", addr, err)
				continue
			}
			u.moved(intro, addr)
			if report {
				reported = true
				d.report(sc, signal.Outcome{Path: signal.PathUpgraded, MS: min(time.Since(u.start).Milliseconds(), signal.MaxOutcomeMS),
					NAT: string(natType), PeerNAT: string(intro.Peer.NAT)})
			}
			return true, nil
		case <-deadline.C:
			if lastErr != nil {
				return false, lastErr
			}
			return false, fmt.Errorf("%w: %s", nat.ErrNoPath, nat.Explain(natType, intro.Peer.NAT, predicted))
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

// maxPathTries bounds the paths one attempt probes.
const maxPathTries = 3

// migrate probes addr as a new path of the session's connection and
// switches to it once validated.
func (u *upgrader) migrate(ctx context.Context, addr netip.AddrPort) error {
	if u.tr == nil {
		u.pc = u.sock.NewPathConn()
		u.tr = &quic.Transport{Conn: u.pc}
		pc, tr := u.pc, u.tr
		context.AfterFunc(u.conn.Context(), func() {
			tr.Close()
			pc.Close()
		})
	}
	u.pc.SetTarget(addr)
	path, err := u.q.AddPath(u.tr)
	if err != nil {
		// The host disabled migration (no RemoHop host does).
		return &errNoMigration{err}
	}
	pctx, cancel := context.WithTimeout(ctx, u.timing.probe)
	defer cancel()
	if err := path.Probe(pctx); err != nil {
		path.Close()
		return fmt.Errorf("not validated: %w", err)
	}
	if err := path.Switch(); err != nil {
		path.Close()
		return err
	}
	return nil
}

// moved records that the session now runs over addr.
func (u *upgrader) moved(intro *signal.Introduction, addr netip.AddrPort) {
	d, t := u.d, u.t
	path := classify(addr, intro.Peer)
	if old := InfoOf(u.conn); old != nil {
		info := *old
		info.Path, info.Remote = path, addr
		u.conn.SetValue(infoKey{}, &info)
	}
	after := time.Since(u.start).Round(time.Second)
	d.log.Info("relayed session moved to a direct path after "+after.String(), "target", t.canonical, "path", path, "remote", addr, "attempt", u.attempts)
	// Punching works between these networks now: the next dial need not
	// set up the relay at once.
	setRelayHint(d.dir, t.canonical, intro.Reflexive, intro.Peer.Reflexive, false)
	time.AfterFunc(u.timing.drain, u.release)
}

// netPrint summarises the machine's addresses for spotting a network change:
// the IPv4 addresses and IPv6 /64s (temporary IPv6 addresses come and go
// within their /64).
func netPrint(addrs []netip.Addr) string {
	keys := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if a.Is6() {
			a = netip.PrefixFrom(a, 64).Masked().Addr()
		}
		keys = append(keys, a.String())
	}
	slices.Sort(keys)
	return strings.Join(slices.Compact(keys), ",")
}
