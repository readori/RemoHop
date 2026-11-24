package mobile

import (
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/hostd"
)

// "Remember this computer" against a host that does not allow pairing
// (remohop host --no-pairing, as the demo and quick-support hosts run):
// the session works, the page learns why nothing was remembered from a
// stable code, and the saved host stays one that needs its password.
func TestRememberRefusedByHost(t *testing.T) {
	initDir(t)
	h := runHost(t, hostd.Config{Password: hostPassword, NoPairing: true})
	c := pageOn(t)
	var att struct {
		Attempt string
		State   string
		Conn    *struct {
			Key    string
			Paired bool
		}
		Pair *struct {
			OK           bool
			Code, Reason string
		}
	}
	c.call("POST", "connect", map[string]any{"target": h.Addr().String(), "password": hostPassword, "pair": true}, &att)
	for deadline := time.Now().Add(20 * time.Second); att.State != "connected"; {
		if att.State == "failed" || time.Now().After(deadline) {
			t.Fatalf("connect: %+v", att)
		}
		time.Sleep(50 * time.Millisecond)
		c.call("GET", "connect/"+att.Attempt, nil, &att)
	}
	if att.Conn == nil || att.Conn.Paired {
		t.Errorf("connection marked paired: %+v", att.Conn)
	}
	if att.Pair == nil || att.Pair.OK || att.Pair.Code != "pair_refused" || att.Pair.Reason != "no_pairing" {
		t.Errorf("pair outcome: %+v", att.Pair)
	}
	c.listFiles(att.Conn.Key)
	var peers struct {
		Peers []struct {
			Target string
			Paired bool
		}
	}
	c.call("GET", "peers", nil, &peers)
	found := false
	for _, p := range peers.Peers {
		if p.Target == h.Addr().String() {
			found = true
			if p.Paired {
				t.Errorf("saved host is paired: %+v", p)
			}
		}
	}
	if !found {
		t.Errorf("the host is not among the saved ones: %+v", peers.Peers)
	}
}
