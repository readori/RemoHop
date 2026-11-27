package hostd_test

import (
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/svc/clip"
)

// A clip grant gives the clipboard as it is, not the host's history of
// it: that holds what the owner and every other session copied.
func TestClipGrantCannotReadHistory(t *testing.T) {
	h, dir := ownedHost(t, hostd.Config{})
	full, err := dialHost(t, h, h.Password())
	if err != nil {
		t.Fatal(err)
	}
	defer full.Close()
	ctx := testCtx(t)
	if err := clip.Set(ctx, full, clip.Text("owner-secret-123")); err != nil {
		t.Skipf("no clipboard on this machine: %v", err)
	}
	if es, err := clip.RemoteHistory(ctx, full, 10); err != nil || len(es) == 0 {
		t.Skipf("the host keeps no clipboard history here: %v %d", err, len(es))
	}
	conn, _, err := redeem(t, h, dir, hostd.GrantRequest{Name: "clipper", For: time.Hour, Allow: []string{"clip"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = clip.RemoteHistory(ctx, conn, 10)
	wantRefusal(t, err, "clipboard history", "clipper")
	es, _ := clip.RemoteHistory(ctx, full, 10)
	_, err = clip.RemoteHistoryGet(ctx, conn, es[0].ID)
	wantRefusal(t, err, "clipboard history")
	wantRefusal(t, clip.RemoteHistoryRestore(ctx, conn, es[0].ID), "clipboard history")
	// The clipboard itself is what the grant gives.
	if _, err := clip.Get(ctx, conn); err != nil {
		t.Fatalf("get under the clip grant: %v", err)
	}
}
