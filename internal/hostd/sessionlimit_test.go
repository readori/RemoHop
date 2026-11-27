package hostd_test

import (
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/perm"
)

// The owner lets one client only watch; it asks for control, is refused,
// asks again too soon, then asks after the cool-down and is allowed. The
// owner can also narrow and lift a live session's limit.
func TestSessionLimit(t *testing.T) {
	asked := make(chan hostd.Approval, 8)
	h, _ := ownedHost(t, hostd.Config{AskTimeout: 5 * time.Second, Overrides: &hostd.Overrides{AskAll: true},
		OnApproval: func(a hostd.Approval) { asked <- a }})
	go func() {
		a := <-asked
		if err := h.DecideWith(a.N, true, []perm.Feature{perm.Screen, perm.Info}); err != nil {
			t.Error(err)
		}
	}()
	conn, err := dialHost(t, h, h.Password())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Screen yes, input no (the desktop service asks Allowed), shell no.
	if v, err := open(t, conn, svcScreen); err != nil || v != false {
		t.Fatalf("view-only session: screen open %v, input allowed %v", err, v)
	}
	if _, err := open(t, conn, svcShellish); err == nil || !strings.Contains(err.Error(), "did not allow this session") {
		t.Fatalf("view-only session opened a shell: %v", err)
	}
	sess := h.Sessions()
	if len(sess) != 1 {
		t.Fatalf("%d sessions", len(sess))
	}
	if all, fs := sess[0].Features(); all || len(fs) != 2 {
		t.Fatalf("Features() = %v %v", all, fs)
	}

	// Asking for control: refused.
	go func() {
		a := <-asked
		if a.Session != sess[0].ID() || len(a.Access) != 1 || a.Access[0] != perm.Input {
			t.Errorf("access approval %+v", a)
		}
		h.Decide(a.N, false)
	}()
	if _, err := hostd.RequestAccess(testCtx(t), conn, []perm.Feature{perm.Input}); err == nil || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("refused request: %v", err)
	}
	// At once again: not even asked.
	if _, err := hostd.RequestAccess(testCtx(t), conn, []perm.Feature{perm.Input}); err == nil || !strings.Contains(err.Error(), "wait") {
		t.Fatalf("second request within the cool-down: %v", err)
	}
	if len(asked) != 0 {
		t.Fatal("the owner was asked during the cool-down")
	}
	// Unknown features are refused outright; ones it has are no question.
	if _, err := hostd.RequestAccess(testCtx(t), conn, []perm.Feature{"teleport"}); err == nil {
		t.Fatal("unknown feature accepted")
	}

	// The owner widens the limit themselves; the session sees it at once.
	if err := h.SetSessionFeatures(sess[0].ID(), false, []perm.Feature{perm.Screen, perm.Info, perm.Input}); err != nil {
		t.Fatal(err)
	}
	if v, err := open(t, conn, svcScreen); err != nil || v != true {
		t.Fatalf("after widening: input allowed %v, %v", v, err)
	}
	r, err := hostd.RequestAccess(testCtx(t), conn, []perm.Feature{perm.Input})
	if err != nil || r.All || len(r.Features) != 3 {
		t.Fatalf("asking for what it has: %+v %v", r, err)
	}
	if len(asked) != 0 {
		t.Fatal("the owner was asked about a feature the session already has")
	}
	// Lifting the limit.
	if err := h.SetSessionFeatures(sess[0].ID(), true, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := open(t, conn, svcShellish); err != nil {
		t.Fatalf("after lifting the limit: %v", err)
	}
	if err := h.SetSessionFeatures(9999, true, nil); err == nil {
		t.Fatal("SetSessionFeatures of a session that does not exist")
	}
}

// An allowed access request widens the limit by what was asked.
func TestSessionAccessAllowed(t *testing.T) {
	asked := make(chan hostd.Approval, 8)
	h, _ := ownedHost(t, hostd.Config{AskTimeout: 5 * time.Second, Overrides: &hostd.Overrides{AskAll: true},
		OnApproval: func(a hostd.Approval) { asked <- a }})
	go func() { h.DecideWith((<-asked).N, true, []perm.Feature{perm.Screen}) }()
	conn, err := dialHost(t, h, h.Password())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	go func() { h.Decide((<-asked).N, true) }()
	r, err := hostd.RequestAccess(testCtx(t), conn, []perm.Feature{perm.Input, perm.Shell})
	if err != nil || len(r.Features) != 3 {
		t.Fatalf("allowed request: %+v %v", r, err)
	}
	if v, err := open(t, conn, svcScreen); err != nil || v != true {
		t.Fatalf("input after an allowed request: %v %v", v, err)
	}
	if _, err := open(t, conn, svcShellish); err != nil {
		t.Fatalf("shell after an allowed request: %v", err)
	}
}

// The control channel lists the host's sessions and ends them (the desktop
// app manages the RemoHop service's sessions this way, elevated).
func TestControlSessions(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	conn, err := dialHost(t, h, h.Password())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ss, err := hostd.ControlSessions(dir)
	if err != nil || len(ss) != 1 || ss[0].ID == 0 || ss[0].Identity == "" || ss[0].Since.IsZero() {
		t.Fatalf("ControlSessions: %+v %v", ss, err)
	}
	if _, err := hostd.ControlDisconnect(dir, ss[0].ID+100); err == nil {
		t.Fatal("disconnecting a session that does not exist")
	}
	if n, err := hostd.ControlDisconnect(dir, ss[0].ID); err != nil || n != 1 {
		t.Fatalf("ControlDisconnect: %d %v", n, err)
	}
	select {
	case <-conn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not end")
	}
	// The host drops the session once its side of the connection has
	// ended too, which may come a moment after the client's.
	deadline := time.Now().Add(5 * time.Second)
	for {
		ss, _ := hostd.ControlSessions(dir)
		if len(ss) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("sessions after disconnect: %+v", ss)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
