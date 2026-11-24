package hostd_test

import (
	"context"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/peer"
)

// loginAll logs in n clients at once with pw and returns how many got in
// and the errors of the others.
func loginAll(t *testing.T, h *hostd.Host, pw string, n int) (int, []error) {
	t.Helper()
	type result struct {
		conn *peer.Conn
		err  error
	}
	results := make(chan result, n)
	for range n {
		go func() {
			c, err := dialHost(t, h, pw)
			results <- result{c, err}
		}()
	}
	ok := 0
	var errs []error
	for range n {
		r := <-results
		if r.err == nil {
			ok++
			r.conn.Close()
		} else {
			errs = append(errs, r.err)
		}
	}
	return ok, errs
}

// Finding 6: a one-time password lets in exactly one of several logins
// that present it at the same time.
func TestOneTimePasswordConcurrentLogins(t *testing.T) {
	h, _ := ownedHost(t, hostd.Config{})
	set(t, hostd.SettingOTP, "on")
	pw := h.Password()
	ok, errs := loginAll(t, h, pw, 6)
	if ok != 1 {
		t.Fatalf("%d of 6 concurrent logins got in with one one-time password, want 1 (errors: %v)", ok, errs)
	}
	for _, err := range errs {
		// A login that proved the password and lost the race is refused
		// as auth_failed; one that tried it after it was spent made a
		// wrong guess, and enough of those lock the source out.
		if c := auth.CodeOf(err); c != auth.CodeAuthFailed && c != auth.CodeLockedOut {
			t.Fatalf("losing login: %v", err)
		}
	}
	if h.Password() == pw {
		t.Fatal("one-time password not replaced")
	}
}

// Finding 6, with approval: several logins prove the same one-time password
// and wait for the owner; approving them all lets only the first in.
// Denial, a client giving up and a manual replacement while a login waits
// all leave the password as they should.
func TestOneTimePasswordWithApproval(t *testing.T) {
	asked := make(chan hostd.Approval, 16)
	h, _ := ownedHost(t, hostd.Config{AskTimeout: 10 * time.Second, OnApproval: func(a hostd.Approval) { asked <- a }})
	set(t, hostd.SettingOTP, "on")
	set(t, hostd.SettingAsk, "on")

	// All approved: one gets in.
	pw := h.Password()
	const n = 3
	results := make(chan error, n)
	for range n {
		go func() {
			c, err := dialHost(t, h, pw)
			if err == nil {
				c.Close()
			}
			results <- err
		}()
	}
	// Every login proves the password before any is let in.
	var waiting []hostd.Approval
	for range n {
		waiting = append(waiting, <-asked)
	}
	for _, a := range waiting {
		go h.Decide(a.N, true)
	}
	ok := 0
	for range n {
		if err := <-results; err == nil {
			ok++
		} else if auth.CodeOf(err) != auth.CodeAuthFailed {
			t.Fatalf("approved login that lost the race: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d of %d approved logins got in with one one-time password, want 1", ok, n)
	}
	if h.Password() == pw {
		t.Fatal("one-time password not replaced")
	}

	// Denied: the password is not spent.
	pw = h.Password()
	go func() { h.Decide((<-asked).N, false) }()
	if _, err := dialHost(t, h, pw); auth.CodeOf(err) != auth.CodeDenied {
		t.Fatalf("denied login: %v", err)
	}
	if h.Password() != pw {
		t.Fatal("a denied login spent the one-time password")
	}

	// The client gives up while waiting: not spent either.
	ctx, cancel := context.WithCancel(testCtx(t))
	gone := make(chan error, 1)
	go func() {
		_, err := dial.Dial(ctx, h.Addr().String(), dial.Options{Dir: t.TempDir(), Password: pw, Locals: noLocals})
		gone <- err
	}()
	a := <-asked
	cancel()
	if err := <-gone; err == nil {
		t.Fatal("cancelled login got in")
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(h.Pending()) > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	h.Decide(a.N, true) // too late; nobody is waiting
	if h.Password() != pw {
		t.Fatal("a cancelled login spent the one-time password")
	}

	// Replaced by hand while a login waits: that login proved a password
	// that is no longer in force and does not get in.
	result2 := make(chan error, 1)
	go func() {
		c, err := dialHost(t, h, pw)
		if err == nil {
			c.Close()
		}
		result2 <- err
	}()
	a = <-asked
	if err := h.RotatePassword(); err != nil {
		t.Fatal(err)
	}
	replaced := h.Password()
	h.Decide(a.N, true)
	if err := <-result2; auth.CodeOf(err) != auth.CodeAuthFailed {
		t.Fatalf("login with a password replaced while it waited: %v", err)
	}
	if h.Password() != replaced {
		t.Fatal("the refused login replaced the new password")
	}
	// And the new one works, once.
	go func() { h.Decide((<-asked).N, true) }()
	if _, err := dialHost(t, h, replaced); err != nil {
		t.Fatalf("new password: %v", err)
	}
	if h.Password() == replaced {
		t.Fatal("one-time password not replaced")
	}
}

// Finding 6: a fixed password next to one-time passwords is never spent,
// however many logins use it at once.
func TestFixedPasswordConcurrentLogins(t *testing.T) {
	h, _ := ownedHost(t, hostd.Config{Password: "tea time at four"})
	set(t, hostd.SettingOTP, "on")
	temp := h.Password()
	ok, errs := loginAll(t, h, "tea time at four", 5)
	if ok != 5 {
		t.Fatalf("%d of 5 fixed-password logins got in: %v", ok, errs)
	}
	if h.Password() != temp {
		t.Fatal("fixed-password logins spent the one-time password")
	}
	set(t, hostd.SettingOTP, "off")
	ok, errs = loginAll(t, h, "tea time at four", 5)
	if ok != 5 {
		t.Fatalf("fixed only: %d of 5 logins got in: %v", ok, errs)
	}
}
