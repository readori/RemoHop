package nat

import "testing"

func TestVirtualInterface(t *testing.T) {
	for name, want := range map[string]bool{
		"eth0": false, "enp3s0": false, "wlan0": false, "en0": false, "Ethernet": false, "Wi-Fi": false,
		"br0": false, "tailscale0": false, "wg0": false, "utun3": false,
		"docker0": true, "br-1a2b3c": true, "veth12ab": true, "virbr0": true, "vmnet8": true, "vboxnet0": true,
		"vEthernet (WSL)": true, "vEthernet (Default Switch)": true, "VMware Network Adapter VMnet1": true,
		"VirtualBox Host-Only Network": true, "bridge100": true, "cni0": true,
	} {
		if got := virtualInterface(name); got != want {
			t.Errorf("virtualInterface(%q) = %v", name, got)
		}
	}
}
