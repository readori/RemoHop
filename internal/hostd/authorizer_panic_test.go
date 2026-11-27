package hostd_test

import (
	"context"
	"strings"
	"testing"

	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/hostd"
)

// `remohop host panic` must hold the grants an authorizer gives, not only
// end their sessions: the same agent must not log straight back in, also
// after the host restarts, until the owner runs `remohop host resume`.
func TestPanicHoldsAuthorizerGrants(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	clientDir, key := keyClient(t, h)
	setAuthz(key, &grant.Grant{Name: "team.ops", Allow: []string{"shell:run-only"}, MaxSessions: 2})
	defer setAuthz(key, nil)
	conn, err := keyLogin(t, h, clientDir)
	if err != nil {
		t.Fatal(err)
	}
	if code, out, errs := runCmd("host", "", "panic"); code != 0 || !strings.Contains(out, "authorizer") {
		t.Fatalf("host panic: %d %s %s", code, out, errs)
	}
	<-conn.Done()
	if _, err := keyLogin(t, h, clientDir); err == nil {
		t.Fatal("an authorizer's key logged back in right after host panic")
	}
	if !hostd.GrantsHeld(dir) {
		t.Fatal("panic left no suspension on disk")
	}

	// A restarted host keeps it.
	h2, err := hostd.New(hostd.Config{Dir: dir, Listen: "127.0.0.1:0", Locals: noLocals})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h2.Run(ctx) }()
	defer func() { cancel(); <-done }()
	clientDir2, key2 := keyClient(t, h2)
	setAuthz(key2, &grant.Grant{Name: "team.ops", Allow: []string{"shell:run-only"}, MaxSessions: 2})
	defer setAuthz(key2, nil)
	if _, err := keyLogin(t, h2, clientDir2); err == nil {
		t.Fatal("an authorizer's key logged in to a restarted host after panic")
	}

	// Resuming one grant by name does not lift it; resuming all does.
	if code, out, _ := runCmd("host", "", "resume", "team.ops"); code != 0 || !strings.Contains(out, "stay on hold") {
		t.Fatalf("resume by name: %d %q", code, out)
	}
	if _, err := keyLogin(t, h, clientDir); err == nil {
		t.Fatal("logged in after resuming one grant by name")
	}
	if code, out, _ := runCmd("host", "", "resume"); code != 0 || !strings.Contains(out, "lifted") {
		t.Fatalf("resume: %d %q", code, out)
	}
	again, err := keyLogin(t, h, clientDir)
	if err != nil {
		t.Fatalf("after resume: %v", err)
	}
	if out, err := run(t, again, "echo back-in", false); err != nil || !strings.Contains(out, "back-in") {
		t.Fatalf("after resume: %q %v", out, err)
	}
}
