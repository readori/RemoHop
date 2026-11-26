package hostd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/perm"
)

// Overrides tighten, for one run of a host, what its config file allows. A
// front end that runs the host for a single sitting (`remohop quick`) sets them
// so that its promises ("you approve every connection") hold whatever
// `remohop config` says. An override can only take away: nothing the config
// file forbids becomes allowed.
type Overrides struct {
	// AskAll makes every login wait for approval at the host, pairings
	// and authorised keys included.
	AskAll bool
	// TempPasswordOnly ignores a fixed password in the config file: only
	// the generated temporary password logs in.
	TempPasswordOnly bool
	// Features, if set, is asked which features are allowed right now;
	// a feature missing from the answer (or false) is off. It is called
	// often and from many goroutines.
	Features func() map[perm.Feature]bool
}

// apply returns p as tightened by o. p's map is shared with the policy
// cache, so it is copied before it is changed.
func (o *Overrides) apply(p policy) policy {
	if o == nil {
		return p
	}
	if o.AskAll {
		p.ask, p.askPaired = true, true
	}
	if o.TempPasswordOnly {
		p.fixed = ""
	}
	if o.Features != nil {
		allowed := o.Features()
		off := make(map[perm.Feature]bool, len(p.off)+len(allowed))
		for f, v := range p.off {
			off[f] = v
		}
		for _, f := range perm.Features() {
			if !allowed[f] {
				off[f] = true
			}
		}
		p.off = off
	}
	return p
}

// ID numbers the session within its host, from 1 in the order sessions
// opened; a front end names a session by it (see Host.Disconnect).
func (s *Session) ID() int { return s.id }

// The reasons a host gives when a person at it ends a session on purpose.
// A client that would otherwise reconnect by itself (the phone apps'
// persistent sessions) must not log straight back in after one of them:
// OwnerEnded. Older hosts send the same words, so the strings stay.
const (
	ReasonDisconnectedAtHost  = "disconnected at the host"         // the desktop app, quick assist
	ReasonDisconnectedByOwner = "disconnected by the host's owner" // remohop host disconnect
	ReasonDisconnectedOnPhone = "disconnected on the phone"        // a phone sharing its screen
	ReasonPairingRevoked      = "pairing revoked"
	reasonPanicPrefix         = "ended by the host's owner"
)

// OwnerEnded reports whether reason, given by a host closing a session,
// says a person there ended it (rather than the host stopping or the
// network failing), so the client must not reconnect.
func OwnerEnded(reason string) bool {
	switch reason {
	case ReasonDisconnectedAtHost, ReasonDisconnectedByOwner, ReasonDisconnectedOnPhone, ReasonPairingRevoked:
		return true
	}
	return strings.HasPrefix(reason, reasonPanicPrefix)
}

// CheckOwnPassword is the rule for a fixed password the owner chooses in a
// front end (the desktop app, a phone's host mode), where every login still
// waits for the owner's answer: at least 8 characters with two of letters,
// digits and other characters, so that it is not a word or a date.
func CheckOwnPassword(pw string) error {
	pw = auth.NormalizePassword(pw)
	if err := auth.CheckPassword(pw); err != nil {
		return err
	}
	var letters, digits, other bool
	for _, r := range pw {
		switch {
		case unicode.IsLetter(r):
			letters = true
		case unicode.IsDigit(r):
			digits = true
		default:
			other = true
		}
	}
	n := 0
	for _, b := range []bool{letters, digits, other} {
		if b {
			n++
		}
	}
	if n < 2 {
		return errors.New("use at least two of: letters, digits, other characters")
	}
	return nil
}

// Disconnect ends the session with the given ID, telling the client why.
func (h *Host) Disconnect(id int, reason string) error {
	for _, s := range h.Sessions() {
		if s.id == id {
			s.conn.CloseWithReason(reason)
			return nil
		}
	}
	return fmt.Errorf("no session %d (it may have ended already)", id)
}

// AlreadyRunningError is returned by StartExclusive when another host
// process is serving the same identity.
type AlreadyRunningError struct{ Status *Status }

func (e *AlreadyRunningError) Error() string {
	return fmt.Sprintf("already running (pid %d); see `remohop host status`", e.Status.PID)
}

// StartExclusive is New for a process that is the machine's host: it
// refuses to start while another host runs on the same identity (the two
// would keep taking the ID away from each other; *AlreadyRunningError),
// records the new host in the status file and keeps that file current.
// Call stop after Run returns; it removes the file. `remohop host` does the same
// by hand.
func StartExclusive(cfg Config) (h *Host, stop func(), err error) {
	dir := cfg.Dir
	if dir == "" {
		if dir, err = config.Dir(); err != nil {
			return nil, nil, err
		}
		cfg.Dir = dir
	}
	statusPath := filepath.Join(dir, statusFile)
	unlock, err := config.Lock(statusPath)
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	if st, _ := ReadStatus(dir); st != nil {
		return nil, nil, &AlreadyRunningError{Status: st}
	}
	if h, err = New(cfg); err != nil {
		return nil, nil, err
	}
	record := func() {
		if err := writeStatus(dir, h.Status()); err != nil {
			h.log.Warn("could not write the status file", "err", err)
		}
	}
	record()
	h.mu.Lock()
	h.record = record
	h.mu.Unlock()
	return h, func() { os.Remove(statusPath) }, nil
}
