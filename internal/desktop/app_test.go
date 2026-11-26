package desktop

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/dial"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/localui"
	"github.com/readori/RemoHop/internal/perm"
	"github.com/readori/RemoHop/internal/signal"
)

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func sessionFeatures(t *testing.T, a *App) (bool, []perm.Feature) {
	t.Helper()
	h, err := a.running()
	if err != nil {
		t.Fatal(err)
	}
	ss := h.Sessions()
	if len(ss) != 1 {
		t.Fatalf("%d sessions", len(ss))
	}
	return ss[0].Features()
}

// The host side of the app: off until switched on, every login asks, the
// answer is per device, a remembered device comes in by itself.
func TestIncoming(t *testing.T) {
	sig := testSignal(t)
	a, _ := startApp(t, "")
	if err := a.FinishSetup(serverCustom, sig, "en"); err != nil {
		t.Fatal(err)
	}
	if st := a.State(); !st.Setup || st.Server.Mode != serverCustom || st.Server.Addr != sig || st.Lang != "en" {
		t.Fatalf("after setup: %+v", st)
	}
	if st := a.State().Incoming; st.On || st.Phase != PhaseOff {
		t.Fatalf("incoming before it was switched on: %+v", st)
	}
	if err := a.SetIncoming(true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "an ID", func() bool { return a.State().Incoming.Phase == PhaseReady })
	in := a.State().Incoming
	id, pw := strings.ReplaceAll(in.ID, " ", ""), in.Password
	helperDir := t.TempDir()
	config.Set(hostd.SettingName, "helper")
	login := func() (chan error, func()) {
		done := make(chan error, 1)
		var closeFn func()
		go func() {
			c, err := dial.Dial(context.Background(), id, dial.Options{Dir: helperDir, Password: pw, Signal: sig})
			if err == nil {
				closeFn = func() { c.Close() }
			}
			done <- err
		}()
		return done, func() {
			if closeFn != nil {
				closeFn()
			}
		}
	}

	// First time: a new device; allowed with control, not files, remembered.
	done, closeFirst := login()
	waitFor(t, "a request", func() bool { return len(a.State().Incoming.Requests) == 1 })
	r := a.State().Incoming.Requests[0]
	if !r.FirstSeen || r.Device != "helper" {
		t.Fatalf("request %+v", r)
	}
	if err := a.Decide(r.N, true, true, false, true); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	all, fs := sessionFeatures(t, a)
	if all || !slices.Contains(fs, perm.Input) || slices.Contains(fs, perm.File) || !slices.Contains(fs, perm.Screen) {
		t.Fatalf("features after allowing control: %v %v", all, fs)
	}
	s := a.State().Incoming.Sessions
	if len(s) != 1 || !s[0].Control || s[0].Files || !s[0].Trusted {
		t.Fatalf("session %+v", s)
	}
	// Files given on the live session.
	if err := a.SetSessionAccess(s[0].ID, true, true); err != nil {
		t.Fatal(err)
	}
	if _, fs := sessionFeatures(t, a); !slices.Contains(fs, perm.File) || !slices.Contains(fs, perm.Clip) {
		t.Fatalf("features after giving files: %v", fs)
	}
	if err := a.Disconnect(0); err != nil {
		t.Fatal(err)
	}
	closeFirst()
	waitFor(t, "the session to end", func() bool { return len(a.State().Incoming.Sessions) == 0 })

	// Remembered: in without a question, with control and without files.
	done, closeSecond := login()
	if err := <-done; err != nil {
		t.Fatalf("remembered device: %v", err)
	}
	if all, fs := sessionFeatures(t, a); all || !slices.Contains(fs, perm.Input) || slices.Contains(fs, perm.File) {
		t.Fatalf("remembered device's features: %v %v", all, fs)
	}
	closeSecond()
	if len(a.Settings().Trusted) != 1 {
		t.Fatal("not remembered")
	}
	if err := a.ForgetDevice(a.Settings().Trusted[0].Identity); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the session to end", func() bool { return len(a.State().Incoming.Sessions) == 0 })

	// Forgotten: asked again, no longer new; denied.
	done, _ = login()
	waitFor(t, "a request", func() bool { return len(a.State().Incoming.Requests) == 1 })
	r = a.State().Incoming.Requests[0]
	if r.FirstSeen {
		t.Fatal("a device that asked before is shown as new")
	}
	if err := a.Decide(r.N, false, false, false, false); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("denied login got in")
	}

	// Off: the host goes.
	if err := a.SetIncoming(false); err != nil {
		t.Fatal(err)
	}
	if st := a.State().Incoming; st.Phase != PhaseOff || st.ID != "" {
		t.Fatalf("after switching off: %+v", st)
	}
	if st, _ := loadSettings(a.Dir()); st.Incoming {
		t.Fatal("the switch was not saved")
	}
}

func TestServerChoice(t *testing.T) {
	a, _ := startApp(t, "")
	if m, _ := a.serverChoice(); m != serverNone {
		t.Fatalf("fresh configuration: %s", m)
	}
	if a.State().Server.Chosen {
		t.Fatal("a fresh configuration counts as chosen")
	}
	for _, c := range []struct{ mode, addr, code string }{
		{serverOfficial, "", ""},
		{serverCustom, "signal.example.net:21116", ""},
		{serverCustom, "", "no_server"},
		{serverCustom, "not a server", "bad_server"},
		{serverCustom, "example.net:99999", "bad_server"},
		{"bogus", "", "bad_request"},
		{serverNone, "", ""},
	} {
		err := a.SetServer(c.mode, c.addr)
		code := ""
		if e, ok := errors.AsType[*localui.Error](err); ok {
			code = e.Code
		}
		if code != c.code {
			t.Errorf("SetServer(%q, %q) = %v, want code %q", c.mode, c.addr, err, c.code)
			continue
		}
		if err == nil {
			mode, addr := a.serverChoice()
			if mode != c.mode {
				t.Errorf("after SetServer(%q): %s %s", c.mode, mode, addr)
			}
			// Quick support shares the choice, "none" included.
			none, _ := config.Get(signal.SettingNone)
			if (none == "on") != (c.mode == serverNone) || !a.State().Server.Chosen {
				t.Errorf("after SetServer(%q): signal-none %q, chosen %v", c.mode, none, a.State().Server.Chosen)
			}
		}
	}
	if v, _ := config.Get("signal"); v != "" {
		t.Fatalf("none left %q", v)
	}
}

func TestStrongPassword(t *testing.T) {
	for pw, ok := range map[string]bool{
		"short":                    false,
		"alllowercaseletters":      false,
		"Abcdefgh1234":             true,
		"abcd-efgh-ijkl-mnop-qrst": true, // 20 letters
		"Abc-def":                  false,
	} {
		if err := CheckStrongPassword(pw); (err == nil) != ok {
			t.Errorf("CheckStrongPassword(%q) = %v", pw, err)
		}
	}
}

func TestSettingsLists(t *testing.T) {
	var s Settings
	now := time.Now()
	if s.see("a", now) {
		t.Fatal("new device seen before")
	}
	if !s.see("a", now) {
		t.Fatal("device not remembered as seen")
	}
	for i := 0; i < maxSeen+10; i++ {
		s.see(strings.Repeat("x", i+1), now.Add(time.Duration(i)*time.Second))
	}
	if len(s.Seen) != maxSeen {
		t.Fatalf("%d seen devices kept", len(s.Seen))
	}
	s.trust(TrustedDevice{Identity: "k", Control: true})
	s.trust(TrustedDevice{Identity: "k", Files: true})
	if len(s.Trusted) != 1 || s.Trusted[0].Control {
		t.Fatalf("trusting again replaces: %+v", s.Trusted)
	}
	if !s.forget("k") || s.forget("k") {
		t.Fatal("forget")
	}
	if normLang("zh-CN") != "zh" || normLang("en_US") != "en" || normLang("fr") != "" || normTheme("blue") != "" {
		t.Fatal("normalising")
	}
}

func TestTargetsAndSIDs(t *testing.T) {
	for _, bad := range []string{"a/b", "x?y", "u@h", "a b", strings.Repeat("a", 300)} {
		if checkTarget(bad) == nil {
			t.Errorf("target %q accepted", bad)
		}
	}
	for _, good := range []string{"123456789", "office-pc", "10.0.0.5:21118", "[::1]:21118"} {
		if err := checkTarget(good); err != nil {
			t.Errorf("target %q: %v", good, err)
		}
	}
	if !validSID("AbCdEfGhIjKlMnOp_-12") || validSID("short") || validSID("has space in it here") {
		t.Fatal("validSID")
	}
	if wakeName("10.0.0.5:21118", "") != "" || wakeName("123456789", "") != "123456789" || wakeName("123456789", "office") != "office" {
		t.Fatal("wakeName")
	}
}

// The UI server answers the page and refuses everything else; ConsoleURL
// points at the gateway in embed mode, and the gateway lets exactly the
// page frame it.
func TestServerAndConsole(t *testing.T) {
	a, srv := startApp(t, "")
	u, err := a.ConsoleURL("123 456 789")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u, "connect=123456789") || !strings.Contains(u, "embed=1") || !strings.Contains(u, "parent=") {
		t.Fatalf("console URL lacks its parameters")
	}
	resp, err := http.Get(strings.SplitN(u, "?", 2)[0])
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors "+srv.Origin()+";") || resp.Header.Get("X-Frame-Options") != "" {
		t.Fatalf("gateway CSP %q", csp)
	}
	resp, err = http.Get(srv.URL())
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	gw := strings.SplitN(u, "?", 2)[0]
	gwOrigin := gw[:strings.Index(gw[7:], "/")+7]
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "frame-src "+gwOrigin+";") {
		t.Fatalf("page CSP %q (gateway %s)", csp, gwOrigin)
	}
	if _, err := a.ConsoleURL("bad/target"); err == nil {
		t.Fatal("bad target accepted")
	}
}

func TestInDir(t *testing.T) {
	dir := t.TempDir()
	for p, want := range map[string]bool{
		dir + "/unattended-1/result.json": true,
		dir + "/../evil":                  false,
		dir:                               false,
		"relative/result.json":            false,
	} {
		if inDir(dir, p) != want {
			t.Errorf("inDir(%q) = %v", p, !want)
		}
	}
}

// The user's own password for incoming connections: refused when weak,
// used at once by the running host and after a restart, shown in the
// state as "fixed"; "" goes back to a temporary password.
func TestOwnPassword(t *testing.T) {
	a, _ := startApp(t, "")
	if err := a.SetIncoming(true); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the host", func() bool { return a.State().Incoming.Password != "" })
	if in := a.State().Incoming; in.PasswordMode != "temporary" {
		t.Fatalf("mode %q", in.PasswordMode)
	}
	if e, ok := errors.AsType[*localui.Error](a.SetOwnPassword("password")); !ok || e.Code != "weak_own_password" {
		t.Fatalf("a weak password: %v", e)
	}
	if err := a.SetOwnPassword("Garden-42-Ok"); err != nil {
		t.Fatal(err)
	}
	h, _ := a.running()
	if in := a.State().Incoming; in.PasswordMode != "fixed" || in.Password != "Garden-42-Ok" || h.PasswordMode() != "fixed" {
		t.Fatalf("after setting: %+v, host %s", in, h.PasswordMode())
	}
	// A new run of the app's host keeps it.
	a.SetIncoming(false)
	a.SetIncoming(true)
	waitFor(t, "the host again", func() bool { h, err := a.running(); return err == nil && h.PasswordMode() == "fixed" })
	if b, _ := os.ReadFile(filepath.Join(a.dir, settingsFile)); !strings.Contains(string(b), "own_password") {
		t.Fatal("not saved")
	}
	if err := a.SetOwnPassword(""); err != nil {
		t.Fatal(err)
	}
	h, _ = a.running()
	if in := a.State().Incoming; in.PasswordMode != "temporary" || h.PasswordMode() != "temporary" || in.Password == "" {
		t.Fatalf("back to temporary: %+v", in)
	}
}
