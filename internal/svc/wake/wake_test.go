package wake

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/wire"
)

func TestParseMAC(t *testing.T) {
	good := []string{
		"aa:bb:cc:dd:ee:01", "AA-BB-CC-DD-EE-01", "aabbccddee01", "aabb.ccdd.ee01", " aa:bb:cc:dd:ee:01 ",
	}
	for _, s := range good {
		mac, err := ParseMAC(s)
		if err != nil || mac.String() != "aa:bb:cc:dd:ee:01" {
			t.Errorf("ParseMAC(%q) = %v, %v", s, mac, err)
		}
	}
	bad := []string{
		"", "aa:bb:cc:dd:ee", "aa:bb:cc:dd:ee:gg", "aa:bb-cc:dd-ee:01", "aabbccddee0", "00:00:00:00:00:00",
		"ff:ff:ff:ff:ff:ff", "aa:bb:cc:dd:ee:01:02", strings.Repeat("a", 5000), "aa:bb:cc:dd:ee:0\n",
		"aabbccddeeff00", "a:b:c:d:e:f",
	}
	for _, s := range bad {
		if mac, err := ParseMAC(s); err == nil {
			t.Errorf("ParseMAC(%q) = %v, want error", s, mac)
		} else if len(err.Error()) > 200 || strings.ContainsAny(err.Error(), "\n\x00") {
			t.Errorf("ParseMAC(%q) error not bounded/sanitised: %q", s, err)
		}
	}
}

func TestPacket(t *testing.T) {
	mac, _ := ParseMAC("01:23:45:67:89:ab")
	p := Packet(mac)
	if len(p) != 102 {
		t.Fatalf("len = %d, want 102", len(p))
	}
	if !bytes.Equal(p[:6], bytes.Repeat([]byte{0xff}, 6)) {
		t.Errorf("bad sync stream % x", p[:6])
	}
	for i := range 16 {
		if !bytes.Equal(p[6+6*i:12+6*i], mac) {
			t.Errorf("repetition %d wrong", i)
		}
	}
}

func listen(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestSendLoopback(t *testing.T) {
	l := listen(t)
	port := l.LocalAddr().(*net.UDPAddr).Port
	res, err := Send(context.Background(), "aa-bb-cc-dd-ee-01", Options{Broadcast: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatal(err)
	}
	if res.MAC != "aa:bb:cc:dd:ee:01" || res.Port != port || len(res.Targets) != 1 || res.Targets[0] != "127.0.0.1" {
		t.Errorf("result = %+v", res)
	}
	buf := make([]byte, 512)
	l.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _, err := l.ReadFromUDP(buf)
	if err != nil {
		t.Fatal(err)
	}
	mac, _ := ParseMAC("aa:bb:cc:dd:ee:01")
	if !bytes.Equal(buf[:n], Packet(mac)) {
		t.Errorf("received % x", buf[:n])
	}
	// More than one: a single broadcast frame is easily lost.
	for i := 1; i < sendRepeats; i++ {
		if n, _, err := l.ReadFromUDP(buf); err != nil || !bytes.Equal(buf[:n], Packet(mac)) {
			t.Fatalf("magic packet %d: %v", i+1, err)
		}
	}
}

func TestSendRejects(t *testing.T) {
	ctx := context.Background()
	for _, o := range []Options{{Broadcast: "nope"}, {Broadcast: "::1"}, {Port: -1}, {Port: 70000}} {
		if _, err := Send(ctx, "aa:bb:cc:dd:ee:01", o); err == nil {
			t.Errorf("Send with %+v succeeded", o)
		}
	}
	if _, err := Send(ctx, "bogus", Options{}); err == nil {
		t.Error("bad MAC accepted")
	}
}

func TestInterfaceBroadcastsAreDirected(t *testing.T) {
	for _, d := range interfaceBroadcasts() {
		if d.dst.To4() == nil || d.src.To4() == nil || d.dst.IsLoopback() || d.dst.Equal(d.src) {
			t.Errorf("odd destination %+v", d)
		}
	}
}

func stubSend(t *testing.T) *[]Options {
	t.Helper()
	var got []Options
	old := sendFn
	sendFn = func(_ context.Context, mac string, o Options) (*Result, error) {
		got = append(got, o)
		hw, err := ParseMAC(mac)
		if err != nil {
			return nil, err
		}
		p, _ := normalizePort(o.Port)
		return &Result{MAC: hw.String(), Port: p, Targets: []string{"192.168.7.255"}}, nil
	}
	t.Cleanup(func() { sendFn = old })
	limiter.Lock()
	limiter.n = 0
	limiter.Unlock()
	return &got
}

func TestRemote(t *testing.T) {
	got := stubSend(t)
	client, _ := peertest.Pair(t)
	res, err := Remote(context.Background(), client, "AABBCCDDEE01", Options{Port: 7, Broadcast: "8.8.8.8"})
	if err != nil {
		t.Fatal(err)
	}
	if res.MAC != "aa:bb:cc:dd:ee:01" || res.Port != 7 || res.Targets[0] != "192.168.7.255" {
		t.Errorf("result = %+v", res)
	}
	if len(*got) != 1 || (*got)[0].Broadcast != "" || (*got)[0].Port != 7 {
		t.Errorf("host saw %+v: a peer must not choose the destination", *got)
	}
}

func TestRemoteHostRejectsBadRequest(t *testing.T) {
	stubSend(t)
	client, _ := peertest.Pair(t)
	s, err := client.Open(context.Background(), serviceName, map[string]any{"mac": "zz", "port": 9})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var rep reply
	if err := readReply(s, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Error == "" {
		t.Errorf("no error for a bad MAC: %+v", rep)
	}
}

func TestRemoteRateLimit(t *testing.T) {
	stubSend(t)
	client, _ := peertest.Pair(t)
	var failed bool
	for i := 0; i < hostBurst+2; i++ {
		if _, err := Remote(context.Background(), client, "aa:bb:cc:dd:ee:01", Options{}); err != nil {
			failed = strings.Contains(err.Error(), "too many")
		}
	}
	if !failed {
		t.Error("host never rate-limited")
	}
}

func env(t *testing.T, client *peer.Conn, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	e := &cli.Env{Stdout: &out, Stderr: &errb, Stdin: strings.NewReader("")}
	name := "wake"
	if client != nil {
		e.Target = "test"
		e.DialFunc = func(context.Context, *cli.Env) (*peer.Conn, error) { return client, nil }
		name = "wol"
	}
	code := cli.Lookup(name).Run(context.Background(), e, args)
	return code, out.String(), errb.String()
}

func TestCLIWakeLocal(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	l := listen(t)
	port := l.LocalAddr().(*net.UDPAddr).Port
	ps := itoa(port)

	code, out, _ := env(t, nil, "aa:bb:cc:dd:ee:01", "--broadcast", "127.0.0.1", "--port", ps, "--json")
	if code != 0 {
		t.Fatalf("code %d", code)
	}
	var res Result
	if err := json.Unmarshal([]byte(out), &res); err != nil || res.MAC != "aa:bb:cc:dd:ee:01" {
		t.Errorf("json %q: %v", out, err)
	}

	if code, out, _ := env(t, nil, "--save", "nas", "AA-BB-CC-DD-EE-02"); code != 0 || !strings.Contains(out, "aa:bb:cc:dd:ee:02") {
		t.Errorf("save: %d %q", code, out)
	}
	if code, out, _ := env(t, nil, "--list"); code != 0 || !strings.Contains(out, "nas") {
		t.Errorf("list: %d %q", code, out)
	}
	if code, out, _ := env(t, nil, "--broadcast", "127.0.0.1", "--port", ps, "nas"); code != 0 || !strings.Contains(out, "aa:bb:cc:dd:ee:02") {
		t.Errorf("wake by name: %d %q", code, out)
	}
	buf := make([]byte, 512)
	l.SetReadDeadline(time.Now().Add(5 * time.Second))
	for range 2 {
		if _, _, err := l.ReadFromUDP(buf); err != nil {
			t.Fatal(err)
		}
	}
	if code, _, e := env(t, nil, "ghost"); code != cli.ExitUsage || !strings.Contains(e, "saved name") {
		t.Errorf("unknown name: %d %q", code, e)
	}
	if code, _, _ := env(t, nil, "--save", "aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:03"); code != cli.ExitUsage {
		t.Errorf("MAC-like name accepted: %d", code)
	}
	if code, _, _ := env(t, nil, "--forget", "nas"); code != 0 {
		t.Errorf("forget: %d", code)
	}
	if code, _, _ := env(t, nil, "--forget", "nas"); code == 0 {
		t.Error("forgetting twice succeeded")
	}
	if code, _, _ := env(t, nil); code != cli.ExitUsage {
		t.Errorf("no args: %d", code)
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestStoreMode(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REMOHOP_CONFIG_DIR", dir)
	st, err := OpenStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Save("pc", "aabbccddee01"); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, storeFile))
	if err != nil {
		t.Fatal(err)
	}
	// Windows has no unix permission bits; the config directory's ACL
	// protects the file there.
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
	if err := os.WriteFile(filepath.Join(dir, storeFile), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.List(); err == nil {
		t.Error("corrupt store accepted")
	}
}

func TestCLIWol(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	stubSend(t)
	client, _ := peertest.Pair(t)
	code, out, e := env(t, client, "aa:bb:cc:dd:ee:01", "--port", "7")
	if code != 0 || !strings.Contains(out, "192.168.7.255") || !strings.Contains(out, "udp/7") {
		t.Fatalf("%d %q %q", code, out, e)
	}
	if code, _, _ := env(t, client, "nonsense!"); code != cli.ExitUsage {
		t.Errorf("bad arg: %d", code)
	}
	if code, out, _ := env(t, client, "--json", "aa:bb:cc:dd:ee:01"); code != 0 || !strings.Contains(out, `"targets"`) {
		t.Errorf("json: %d %q", code, out)
	}
}

func readReply(s peer.Stream, v *reply) error { return wire.ReadJSON(s, v) }
