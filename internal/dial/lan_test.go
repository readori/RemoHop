package dial

import (
	"crypto/ed25519"
	"net/netip"
	"testing"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/lan"
)

func TestAnnotateLAN(t *testing.T) {
	key := func() (string, string) {
		pub, _, _ := ed25519.GenerateKey(nil)
		return auth.EncodeKey(pub), auth.Fingerprint(pub)
	}
	studioKey, studioFP := key()
	otherKey, _ := key()
	_, selfFP := key()
	addr := netip.MustParseAddrPort("192.168.1.20:21118")
	peers := []Peer{
		{Target: "418203775", Alias: "studio", HostKey: studioKey, TokenID: "t", Token: []byte("x")},
		{Target: "192.168.1.20:21118", HostKey: studioKey},
		{Target: "500600700", HostKey: otherKey},          // another host's ID
		{Target: "192.168.1.30:21118", HostKey: otherKey}, // another host's address
		{Target: "600700800"}, // no key pinned yet
	}
	hosts := []lan.Host{
		{Name: "studio", Addr: addr, ID: "418203775", Fingerprint: studioFP},
		{Name: "impostor", Addr: netip.MustParseAddrPort("192.168.1.66:1"), ID: "500600700", Fingerprint: "aaaa:aaaa:aaaa:aaaa:aaaa"},
		{Name: "new", Addr: netip.MustParseAddrPort("192.168.1.30:21118"), Fingerprint: "bbbb:bbbb:bbbb:bbbb:bbbb"},
		{Name: "unpinned", Addr: netip.MustParseAddrPort("192.168.1.31:1"), ID: "600700800", Fingerprint: "cccc:cccc:cccc:cccc:cccc"},
		{Name: "me", Addr: netip.MustParseAddrPort("192.168.1.2:1"), Fingerprint: selfFP},
	}
	got := annotateLAN(hosts, peers, selfFP)
	s := got[0]
	if len(s.Saved) != 2 || s.Saved[0] != (LANSaved{Target: "418203775", Alias: "studio", Paired: true}) || s.Saved[1].Paired || s.Conflict != "" || s.Self {
		t.Errorf("studio: %+v", s)
	}
	if got[1].Conflict != "500600700" || len(got[1].Saved) != 0 {
		t.Errorf("a host claiming a saved ID with another key: %+v", got[1])
	}
	if got[2].Conflict != "192.168.1.30:21118" {
		t.Errorf("a saved address with another key: %+v", got[2])
	}
	if got[3].Conflict != "" || len(got[3].Saved) != 0 {
		t.Errorf("a saved ID without a pinned key: %+v", got[3])
	}
	if !got[4].Self || got[0].Self {
		t.Errorf("this machine: %+v", got[4])
	}
}
