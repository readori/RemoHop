package account

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/readori/RemoHop/internal/account/ledger"
	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/signal"
)

// RequestCode asks the rendezvous server to mail a login code to email.
func RequestCode(ctx context.Context, opts Options, email string) error {
	email, err := ledger.NormalizeEmail(email)
	if err != nil {
		return err
	}
	addr, sopts, err := signal.Resolve(opts.Signal)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	s, err := dialServer(ctx, opts, addr, sopts)
	if err != nil {
		return err
	}
	defer s.close()
	return s.sc.AccountLogin(ctx, email)
}

// LoginRequest is the input of Login.
type LoginRequest struct {
	Email string
	// Code is the login code from the mail.
	Code string
	// AccountFingerprint, if set, is the account fingerprint an existing
	// device of the account shows (`remohop account`); joining an account
	// with another fingerprint fails.
	AccountFingerprint string
	// ConfirmAccount is asked otherwise, when this device joins an
	// existing account: does the fingerprint match what an existing
	// device of the account shows? With neither, joining fails with
	// ErrConfirmAccount.
	ConfirmAccount func(fingerprint string) (bool, error)
}

// ErrConfirmAccount: joining an existing account needs its fingerprint to
// be confirmed.
var ErrConfirmAccount = errors.New("joining an existing account needs its fingerprint confirmed: " +
	"pass --account-fingerprint with the fingerprint `remohop account` shows on one of your other devices")

// LoginResult describes a successful login.
type LoginResult struct {
	// Status is "created" (this device created the account), "member"
	// (it was already a member) or "pending" (an existing device has to
	// approve it: `remohop account approve DeviceFingerprint` there).
	Status string `json:"status"`
	Email  string `json:"email"`
	// Account is the account fingerprint.
	Account string `json:"account"`
	// Device is this device's identity fingerprint.
	Device string `json:"device"`
}

// Login proves a login code and joins this device to the account.
func Login(ctx context.Context, opts Options, req LoginRequest) (*LoginResult, error) {
	email, err := ledger.NormalizeEmail(req.Email)
	if err != nil {
		return nil, err
	}
	code := signal.NormalizeCode(req.Code)
	if len(code) != signal.CodeDigits {
		return nil, fmt.Errorf("a login code has %d digits", signal.CodeDigits)
	}
	dir, err := opts.dir()
	if err != nil {
		return nil, err
	}
	old, err := Load(dir)
	if err == nil && old.Email != email && old.Status != StatusLoggedOut {
		return nil, fmt.Errorf("this device is logged in as %s; run `remohop logout` first", old.Email)
	} else if err != nil && !errors.Is(err, ErrNotLoggedIn) {
		return nil, err
	}
	id, err := auth.LoadIdentity(dir)
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	addr, sopts, err := signal.Resolve(opts.Signal)
	if err != nil {
		return nil, err
	}
	name, platform := deviceInfo(dir)
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	s, err := dialServer(ctx, opts, addr, sopts)
	if err != nil {
		return nil, err
	}
	defer s.close()
	r, err := s.sc.AccountVerify(ctx, id, email, code, name, platform)
	if err != nil {
		return nil, err
	}
	st := &State{Server: addr, ServerWS: sopts.WS, ServerKey: auth.EncodeKey(s.sc.ServerKey()), Email: email, Token: r.Token,
		LoggedIn: time.Now().UTC().Truncate(time.Second)}
	res := &LoginResult{Email: email, Device: auth.Fingerprint(id.Public())}
	logout := func() { s.sc.AccountCall(context.Background(), id, r.Token, "logout", signal.AccountArgs{}) }

	if r.State == signal.AccountNew {
		create := ledger.Create(id, email, name)
		st.AccountID = create.Hash()
		r2, err := s.sc.AccountCall(ctx, id, r.Token, "append", signal.AccountArgs{Entry: &create})
		switch {
		case err == nil:
			if _, err := st.adopt(r2.Log); err != nil {
				logout()
				return nil, err
			}
			st.Status = StatusMember
			res.Status, res.Account = "created", st.Fingerprint()
			if err := save(dir, st); err != nil {
				return nil, err
			}
			return res, nil
		case signal.CodeOf(err) == signal.CodeConflict:
			// Another device created the account a moment ago.
			if r, err = s.sc.AccountCall(ctx, id, r.Token, "state", signal.AccountArgs{}); err != nil {
				return nil, err
			}
			st.AccountID = nil
		default:
			logout()
			return nil, err
		}
	}
	if len(r.AccountID) != 32 || (r.State != signal.AccountPending && r.State != signal.AccountMember) {
		logout()
		return nil, errors.New("the rendezvous server gave an unexpected answer")
	}
	// Joining an existing account: its ID comes from the server, which
	// is not trusted to name it. A person compares.
	fp := ledger.Fingerprint(r.AccountID)
	confirmed := old != nil && bytes.Equal(old.AccountID, r.AccountID)
	switch {
	case confirmed:
	case req.AccountFingerprint != "":
		want := ledger.NormalizeFingerprint(req.AccountFingerprint)
		if want == "" {
			logout()
			return nil, fmt.Errorf("%q is not an account fingerprint (20 hex digits, like %s)", req.AccountFingerprint, fp)
		}
		if want != fp {
			logout()
			return nil, fmt.Errorf("the account on the rendezvous server has fingerprint %s, not %s: not joining it. "+
				"If %s is what your other device shows, the rendezvous server is not presenting your real account", fp, want, want)
		}
	case req.ConfirmAccount != nil:
		ok, err := req.ConfirmAccount(fp)
		if err != nil || !ok {
			logout()
			if err == nil {
				err = errors.New("account fingerprint not confirmed: not joining")
			}
			return nil, err
		}
	default:
		logout()
		return nil, ErrConfirmAccount
	}
	st.AccountID = r.AccountID
	res.Account = fp
	if old != nil && bytes.Equal(old.AccountID, r.AccountID) {
		st.Log = old.Log // keep what was verified: it must still be extended
	}
	if r.State == signal.AccountMember {
		ls, err := st.adopt(r.Log)
		if err != nil {
			logout()
			return nil, err
		}
		if !ls.IsMember(id.Public()) {
			logout()
			return nil, errors.New("the rendezvous server says this device is a member, but the account's log does not")
		}
		st.Status, res.Status = StatusMember, "member"
	} else {
		st.Status, res.Status = StatusPending, "pending"
	}
	if err := save(dir, st); err != nil {
		return nil, err
	}
	return res, nil
}

// Refresh fetches the account's state from the server and verifies its
// log; for a pending device it reports whether it has been approved.
func Refresh(ctx context.Context, opts Options) (*State, error) {
	dir, err := opts.dir()
	if err != nil {
		return nil, err
	}
	st, err := Load(dir)
	if err != nil {
		return nil, err
	}
	r, err := call(ctx, opts, dir, st, "state", signal.AccountArgs{})
	if err != nil {
		return st, err
	}
	if err := apply(dir, st, r); err != nil {
		return st, err
	}
	return st, nil
}

// apply takes in a server answer: the device's status and, for a member,
// the verified log.
func apply(dir string, st *State, r *signal.AccountReply) error {
	switch r.State {
	case signal.AccountMember, signal.AccountPending:
	default:
		return errors.New("the account on the rendezvous server has no devices any more; log in again")
	}
	if !bytes.Equal(r.AccountID, st.AccountID) {
		return fmt.Errorf("the rendezvous server now presents account %s instead of %s; refusing it",
			ledger.Fingerprint(r.AccountID), st.Fingerprint())
	}
	status := StatusPending
	if r.State == signal.AccountMember {
		id, err := auth.LoadIdentity(dir)
		if err != nil {
			return err
		}
		ls, err := st.adopt(r.Log)
		if err != nil {
			return err
		}
		if !ls.IsMember(id.Public()) {
			return errors.New("the rendezvous server says this device is a member, but the account's log does not")
		}
		status = StatusMember
	} else if st.Status == StatusMember {
		// A member stays one until the log says otherwise.
		return errors.New("the rendezvous server says this device is no member, but its log does; ignoring it")
	}
	st.Status = status
	log, verified := st.Log, st.Verified
	return commit(dir, st.Token, func(cur *State) error {
		cur.Status, cur.Log, cur.Verified = status, log, verified
		return nil
	})
}

// WaitForApproval polls the server until this pending device has been
// approved, ctx ends, or the session fails.
func WaitForApproval(ctx context.Context, opts Options, every time.Duration) (*State, error) {
	if every <= 0 {
		every = 2 * time.Second
	}
	for {
		st, err := Refresh(ctx, opts)
		if err != nil && (errors.Is(err, ErrNotLoggedIn) || signal.CodeOf(err) == signal.CodeNotLoggedIn) {
			return st, err
		}
		if err == nil && st.Status == StatusMember {
			return st, nil
		}
		select {
		case <-ctx.Done():
			return st, ctx.Err()
		case <-time.After(every):
		}
	}
}

// Logout removes this device from the account (an entry signed by
// itself), ends its session and forgets the account here, including the
// keys account-trust had authorised. If the server cannot be reached the
// account is forgotten here anyway and the error says so: the device then
// stays in the account's list until another device removes it.
func Logout(ctx context.Context, opts Options) error {
	dir, err := opts.dir()
	if err != nil {
		return err
	}
	st, err := Load(dir)
	if err != nil {
		return err
	}
	var serverErr error
	if st.Status == StatusMember {
		serverErr = removeDevice(ctx, opts, dir, st, nil)
	}
	if serverErr == nil || signal.CodeOf(serverErr) == signal.CodeNotLoggedIn {
		if _, err := call(ctx, opts, dir, st, "logout", signal.AccountArgs{}); err != nil && signal.CodeOf(err) != signal.CodeNotLoggedIn {
			serverErr = err
		}
	}
	if err := remove(dir); err != nil {
		return err
	}
	if _, _, err := SyncTrust(dir); err != nil {
		return err
	}
	if serverErr != nil && signal.CodeOf(serverErr) != signal.CodeNotLoggedIn {
		return fmt.Errorf("logged out here, but the rendezvous server was not told (%w); this device stays in the "+
			"account's list until another device runs `remohop account remove %s`", serverErr, auth.Fingerprint(mustIdentity(dir)))
	}
	return nil
}

func mustIdentity(dir string) []byte {
	id, err := auth.LoadIdentity(dir)
	if err != nil {
		return nil
	}
	return id.Public()
}

// Delete deletes the account on the server: every device's session ends
// and the account is gone. Only a member can.
func Delete(ctx context.Context, opts Options) error {
	dir, err := opts.dir()
	if err != nil {
		return err
	}
	st, err := Load(dir)
	if err != nil {
		return err
	}
	if _, err := call(ctx, opts, dir, st, "delete", signal.AccountArgs{}); err != nil {
		return err
	}
	if err := remove(dir); err != nil {
		return err
	}
	_, _, err = SyncTrust(dir)
	return err
}
