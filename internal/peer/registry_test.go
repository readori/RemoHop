package peer_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/wire"
)

// A wrapped registry serves its base's services, including ones added
// later, through the wrapper; its own services are served as they are.
func TestRegistryWrapped(t *testing.T) {
	base := peer.NewRegistry()
	reply := func(v string) peer.Handler {
		return func(_ context.Context, _ *peer.Conn, s peer.Stream, _ json.RawMessage) error {
			return wire.WriteJSON(s, v)
		}
	}
	base.Handle("a", reply("a"))
	var wrapped []string
	reg := base.Wrapped(func(name string, h peer.Handler) peer.Handler {
		return func(ctx context.Context, c *peer.Conn, s peer.Stream, req json.RawMessage) error {
			wrapped = append(wrapped, name)
			return h(ctx, c, s, req)
		}
	})
	reg.Handle("own", reply("own"))
	base.Handle("later", reply("later"))
	if got := reg.Names(); !slices.Equal(got, []string{"a", "later", "own"}) {
		t.Fatalf("names %v", got)
	}
	client, _ := peertest.PairWith(t, reg)
	for _, name := range []string{"a", "later", "own"} {
		s, err := client.Open(context.Background(), name, nil)
		if err != nil {
			t.Fatal(err)
		}
		var v string
		if err := wire.ReadJSON(s, &v); err != nil || v != name {
			t.Fatalf("%s: %q %v", name, v, err)
		}
		s.Close()
	}
	if _, err := client.Open(context.Background(), "missing", nil); err == nil {
		t.Fatal("unknown service opened")
	}
	if !slices.Equal(wrapped, []string{"a", "later"}) {
		t.Fatalf("wrapped %v", wrapped)
	}
}
