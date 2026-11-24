package grant_test

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
)

// Missing grant state on the host side of a session means no access, not
// full access; AttachFull is how full access is said.
func TestOfFailsClosed(t *testing.T) {
	client, host := peertest.PairOpts(t, peer.NewRegistry(), peertest.Options{NoStart: true})
	if g := grant.Of(host); g != nil {
		t.Fatalf("peertest host (marked full access): guard %v", g)
	}
	// A host-side connection nobody set up.
	bare := peer.New(nil, peer.Options{Role: peer.RoleHost})
	g := grant.Of(bare)
	if g == nil {
		t.Fatal("a host session without grant state has full access")
	}
	ctx := context.Background()
	for name, err := range map[string]error{
		"command":  g.Command(ctx, "exec", "true", false, nil),
		"terminal": g.Terminal(ctx, "main"),
		"file":     g.File(ctx, "open", grant.Read, "/"),
	} {
		if !errors.Is(err, grant.ErrNoGrantState) || !errors.Is(err, fs.ErrPermission) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := grant.ValidName(g.Name()); err == nil {
		t.Errorf("unset guard's name %q is a valid grant name", g.Name())
	}
	if g.Record("exec", "x", 80, 24) != nil {
		t.Error("unset guard records")
	}
	grant.AttachFull(bare)
	if grant.Of(bare) != nil {
		t.Fatal("AttachFull did not mean full access")
	}
	// The controlling side is never restricted by grant state.
	if grant.Of(client) != nil {
		t.Fatal("client side restricted")
	}
	if grant.Of(peer.New(nil, peer.Options{Role: peer.RoleClient})) != nil {
		t.Fatal("bare client side restricted")
	}
}
