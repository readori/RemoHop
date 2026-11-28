package hostd

import (
	"net"
	"strings"
	"testing"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/lan"
)

func TestLANDiscovery(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", dir)
	t.Setenv("REMOHOP_LAN_DISCOVERY", "")
	newHost := func(cfg Config) *Host {
		t.Helper()
		cfg.Dir = dir
		h, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { h.ln.Close(); h.sock.Close() })
		return h
	}
	if h := newHost(Config{Listen: "127.0.0.1:0"}); h.lanCapable() {
		t.Error("a host on loopback answers discovery")
	}
	if h := newHost(Config{Listen: "0.0.0.0:0", NoLANDiscovery: true}); h.lanCapable() {
		t.Error("NoLANDiscovery is not respected")
	}
	h := newHost(Config{Listen: "0.0.0.0:0", Password: "correct-horse-1"})
	if !h.lanCapable() {
		t.Fatal("a host on every address does not answer discovery")
	}
	if err := config.Set(SettingName, "Studio"); err != nil {
		t.Fatal(err)
	}
	info, on := h.lanInfo()
	if !on || info.Name != "Studio" || info.Fingerprint != auth.Fingerprint(h.PublicKey()) || int(info.Port) != h.sock.LocalAddr().(*net.UDPAddr).Port {
		t.Errorf("announces %+v (on %v)", info, on)
	}
	for _, s := range info.TXT() {
		if strings.Contains(s, h.Password()) || strings.Contains(s, "correct-horse") {
			t.Errorf("TXT carries the password: %q", s)
		}
	}
	// Turned off, while the host runs.
	if err := config.Set(lan.Setting, "off"); err != nil {
		t.Fatal(err)
	}
	if _, on := h.lanInfo(); on {
		t.Error("still answers with lan-discovery off")
	}
	config.Set(lan.Setting, "")
	t.Setenv("REMOHOP_LAN_DISCOVERY", "0")
	h.policies = policyCache{} // the environment is read with the file
	if _, on := h.lanInfo(); on {
		t.Error("still answers with REMOHOP_LAN_DISCOVERY=0")
	}
}
