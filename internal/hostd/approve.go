package hostd

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/perm"
)

const (
	// DefaultAskTimeout is how long a login waits for the owner's answer
	// before it is refused.
	DefaultAskTimeout = 30 * time.Second
	// maxApprovals bounds logins waiting for an answer.
	maxApprovals = 16
)

// Approval is a login that has presented a valid credential and is waiting
// for the host's owner to allow it.
type Approval struct {
	N        int            `json:"n"`
	From     netip.AddrPort `json:"from"`
	Identity string         `json:"identity"`       // fingerprint of the client's key
	Name     string         `json:"name,omitempty"` // pairing or authorised-key name
	// Device is the name the client gave itself (`remohop config name`): a
	// claim, unverified; Identity is what identifies it.
	Device string    `json:"device,omitempty"`
	Auth   auth.Kind `json:"auth"`
	Since  time.Time `json:"since"`
	// Grant and Action are set when an action of a granted session (not a
	// login) waits for approval: the grant's name and what is asked, e.g.
	// "exec: git push".
	Grant  string `json:"grant,omitempty"`
	Action string `json:"action,omitempty"`
	// Access is set when a connected session asks for more of what its
	// owner limited it to (see Host.SetSessionFeatures): the features it
	// asks for; Session is its ID.
	Access  []perm.Feature `json:"access,omitempty"`
	Session int            `json:"session,omitempty"`

	decide chan decision
}

// decision is the owner's answer to an Approval. With limited set, an
// allowed login gets only features (see Host.DecideWith).
type decision struct {
	allow    bool
	limited  bool
	features []perm.Feature
}

// Pending lists the logins waiting for approval, oldest first.
func (h *Host) Pending() []Approval {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]Approval, 0, len(h.approvals))
	for _, a := range h.approvals {
		out = append(out, *a)
	}
	return out
}

// Decide answers pending approval n. n == 0 answers the one request that
// is waiting, and is refused when more than one is: an answer must not
// land on a request other than the one its giver read.
func (h *Host) Decide(n int, allow bool) error {
	_, err := h.decide(n, decision{allow: allow})
	return err
}

// DecideShown is Decide, and returns the request it answered, so that the
// answer can be confirmed with its number and what it was about.
func (h *Host) DecideShown(n int, allow bool) (Approval, error) {
	return h.decide(n, decision{allow: allow})
}

// DecideWith answers pending approval n like Decide, and an allowed login
// gets only the features listed, on top of what the host allows anyway:
// the owner's choice for this one client. The session can ask for more
// later (ServiceAccess), and the owner can change it with
// SetSessionFeatures. For an approval that is not a login, features are
// ignored.
func (h *Host) DecideWith(n int, allow bool, features []perm.Feature) error {
	if n <= 0 {
		return errors.New("no such connection request")
	}
	_, err := h.decide(n, decision{allow: allow, limited: true, features: features})
	return err
}

func (h *Host) decide(n int, d decision) (Approval, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if n == 0 {
		switch len(h.approvals) {
		case 0:
			return Approval{}, errors.New("no request is waiting for approval")
		case 1:
			n = h.approvals[0].N
		default:
			nums := make([]string, len(h.approvals))
			for i, a := range h.approvals {
				nums[i] = strconv.Itoa(a.N)
			}
			return Approval{}, fmt.Errorf("%d requests are waiting (%s); name the one you mean, for example `y %s` or `n %s`",
				len(h.approvals), strings.Join(nums, ", "), nums[len(nums)-1], nums[len(nums)-1])
		}
	}
	for _, a := range h.approvals {
		if a.N == n {
			select {
			case a.decide <- d:
			default:
			}
			return *a, nil
		}
	}
	return Approval{}, fmt.Errorf("no pending request %d (it may have timed out)", n)
}

// needsApproval reports whether the owner wants to be asked about res.
func (p policy) needsApproval(res *auth.Result) bool {
	if res.Scope != "" {
		return false // a limited login a feature vouched for (extend.go)
	}
	return p.ask && (res.Kind == auth.KindPassword || p.askPaired)
}

// ask blocks until the owner has decided about the login, the wait times
// out, or the client goes away (done). Anything but an explicit yes is a
// refusal.
func (h *Host) ask(ctx context.Context, done <-chan struct{}, res *auth.Result, from netip.AddrPort) (decision, error) {
	a := &Approval{From: from, Identity: auth.Fingerprint(res.ClientKey), Name: res.TokenName, Device: res.ClientName, Auth: res.Kind,
		Since: time.Now(), decide: make(chan decision, 1)}
	outcome, d, err := h.wait(ctx, done, a)
	if outcome == "busy" {
		return d, err
	}
	fields := []string{"decision", outcome, "from", from.String(), "identity", a.Identity, "name", a.Name, "device", a.Device, "auth", string(res.Kind)}
	if d.limited && err == nil {
		fields = append(fields, "features", featureList(d.features))
	}
	h.audit.add("approval", fields...)
	h.log.Info("approval "+outcome, "n", a.N, "from", from)
	return d, err
}

// askAction asks the host's owner whether a granted session may perform
// action. Anything but an explicit yes refuses it.
func (h *Host) askAction(ctx context.Context, s *Session, action string) error {
	a := &Approval{From: s.Remote, Identity: auth.Fingerprint(s.Auth.ClientKey), Name: s.Auth.TokenName, Device: s.Auth.ClientName,
		Auth: s.Auth.Kind, Grant: s.grant, Action: action, Since: time.Now(), decide: make(chan decision, 1)}
	decision, _, err := h.wait(ctx, s.conn.Done(), a)
	h.audit.add("approval", s.fields("decision", decision, "action", action)...)
	h.log.Info("action approval "+decision, "n", a.N, "grant", s.grant, "action", action)
	switch decision {
	case "allowed":
		return nil
	case "denied":
		return fmt.Errorf("the host's owner denied: %s (grant %s asks for approval)", action, s.grant)
	case "timeout":
		return fmt.Errorf("nobody approved %q at the host in time, so it was refused (grant %s asks for approval)", action, s.grant)
	}
	return err
}

// wait queues a for the owner's answer and blocks until it comes, the wait
// times out, the client goes away (done) or the host stops. decision is
// "allowed", "denied", "timeout", "client-left" or "host-stopped".
func (h *Host) wait(ctx context.Context, done <-chan struct{}, a *Approval) (outcome string, d decision, err error) {
	h.mu.Lock()
	if len(h.approvals) >= maxApprovals {
		h.mu.Unlock()
		return "busy", d, errors.New("too many connections waiting for approval")
	}
	h.nextApproval++
	a.N = h.nextApproval
	h.approvals = append(h.approvals, a)
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		for i, x := range h.approvals {
			if x == a {
				h.approvals = append(h.approvals[:i], h.approvals[i+1:]...)
				break
			}
		}
		h.mu.Unlock()
	}()

	if a.Action != "" && notifyDesktop != nil {
		go notifyDesktop("RemoHop: approval needed", fmt.Sprintf("Request #%d: grant %s wants to %s. Answer with `remohop host approve %d` or `remohop host deny %d`.",
			a.N, a.Grant, cli.OneLine(a.Action), a.N, a.N))
	}
	h.log.Info("waiting for approval", "n", a.N, "from", a.From, "identity", a.Identity, "name", a.Name, "device", a.Device, "action", a.Action)
	if h.cfg.OnApproval != nil {
		h.cfg.OnApproval(*a)
	}
	timeout := h.cfg.AskTimeout
	if timeout <= 0 {
		timeout = DefaultAskTimeout
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	outcome = "timeout"
	err = errors.New("nobody answered at the host")
	select {
	case d = <-a.decide:
		if d.allow {
			outcome, err = "allowed", nil
		} else {
			outcome, err = "denied", errors.New("denied at the host")
		}
	case <-t.C:
	case <-done:
		outcome, err = "client-left", errors.New("client went away")
	case <-ctx.Done():
		outcome, err = "host-stopped", ctx.Err()
	}
	return outcome, d, err
}
