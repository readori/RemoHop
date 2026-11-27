//go:build unix

package hostd_test

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/svc/shell"
)

// Two identities that an authorizer admits on the same grant (one team
// rule) must not see, read, stop or attach to each other's background jobs
// and terminals, nor use up each other's sessions.
func TestAuthorizerIdentitiesAreKeptApart(t *testing.T) {
	h, _ := ownedHost(t, hostd.Config{})
	dirA, keyA := keyClient(t, h)
	dirB, keyB := keyClient(t, h)
	g := &grant.Grant{Name: "team.dev", Allow: []string{"shell"}, MaxSessions: 1}
	setAuthz(keyA, g)
	setAuthz(keyB, g)
	defer setAuthz(keyA, nil)
	defer setAuthz(keyB, nil)
	a, err := keyLogin(t, h, dirA)
	if err != nil {
		t.Fatal(err)
	}
	// The limit is per identity: A's session leaves B's free.
	b, err := keyLogin(t, h, dirB)
	if err != nil {
		t.Fatalf("B refused while A holds the grant's one session: %v", err)
	}
	if _, err := keyLogin(t, h, dirA); err == nil {
		t.Fatal("A got a second session past the limit")
	}

	ctx := testCtx(t)
	j, err := shell.Spawn(ctx, a, shell.SpawnRequest{Command: "echo alice-secret-token-123", Name: "a-job"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	js, err := shell.Jobs(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range js {
		if x.ID == j.ID {
			t.Fatal("B lists A's job")
		}
	}
	if r, err := shell.Logs(ctx, b, shell.LogsRequest{Job: j.ID}); err == nil {
		out, _ := io.ReadAll(r)
		r.Close()
		if strings.Contains(string(out), "alice-secret-token-123") {
			t.Fatal("B read A's job log")
		}
	}
	if _, err := shell.Kill(ctx, b, "a-job", ""); err == nil {
		t.Fatal("B stopped A's job by name")
	}
	if _, err := shell.RemoveJobs(ctx, b, shell.RemoveRequest{Jobs: []string{j.ID}}); err == nil {
		t.Fatal("B removed A's job")
	}
	// A still has it.
	if r, err := shell.Logs(ctx, a, shell.LogsRequest{Job: j.ID}); err != nil {
		t.Fatalf("A's own log: %v", err)
	} else {
		out, _ := io.ReadAll(r)
		r.Close()
		if !strings.Contains(string(out), "alice-secret-token-123") {
			t.Fatalf("A's own log: %q", out)
		}
	}

	// Terminals likewise.
	if _, err := shell.Attach(ctx, a, shell.AttachRequest{Name: "a-term"}, shell.AttachIO{Stdin: strings.NewReader(""), Stdout: discard{}}); err != nil {
		t.Fatal(err)
	}
	defer shell.KillSession(ctx, a, "a-term")
	ss, err := shell.Sessions(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ss {
		if s.Name == "a-term" {
			t.Fatal("B lists A's terminal")
		}
	}
	if _, err := shell.Attach(ctx, b, shell.AttachRequest{Name: "a-term"}, shell.AttachIO{Stdin: strings.NewReader(""), Stdout: discard{}}); err == nil {
		t.Fatal("B attached to A's terminal")
	}
	if err := shell.KillSession(ctx, b, "a-term"); err == nil {
		t.Fatal("B ended A's terminal")
	}
}
