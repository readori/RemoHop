package hostd_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/perm"
)

func TestStartExclusive(t *testing.T) {
	dir := t.TempDir()
	cfg := hostd.Config{Dir: dir, Listen: "127.0.0.1:0", Locals: noLocals}
	h, release, err := hostd.StartExclusive(cfg)
	if err != nil {
		t.Fatal(err)
	}
	status := filepath.Join(dir, "host.json")
	b, err := os.ReadFile(status)
	if pw, _ := h.TempPassword(); err != nil || !strings.Contains(string(b), pw) {
		t.Fatalf("status file: %v %s", err, b)
	}
	// A second host on the same identity is refused, with the first one's status.
	_, _, err = hostd.StartExclusive(cfg)
	var running *hostd.AlreadyRunningError
	if !errors.As(err, &running) || running.Status.PID != os.Getpid() {
		t.Fatalf("second host: %v", err)
	}
	// The file follows the host.
	if err := h.RotatePassword(); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(status)
	if pw, _ := h.TempPassword(); !strings.Contains(string(b), pw) {
		t.Fatalf("status file not rewritten after the password changed: %s", b)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Run(ctx) }()
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(status); !os.IsNotExist(err) {
		t.Fatalf("status file left behind: %v", err)
	}
}

// Overrides tighten what the config file says and can only take away.
func TestOverrides(t *testing.T) {
	allowed := map[perm.Feature]bool{perm.Screen: true, perm.Shell: true}
	h, _ := ownedHost(t, hostd.Config{Overrides: &hostd.Overrides{AskAll: true, TempPasswordOnly: true,
		Features: func() map[perm.Feature]bool { return allowed }}})
	set(t, hostd.SettingPassword, "a-fixed-password")
	set(t, hostd.FeatureSetting(perm.Shell), "off")
	if h.PasswordMode() != "temporary" {
		t.Fatalf("password mode %q with TempPasswordOnly", h.PasswordMode())
	}
	if _, err := dialHost(t, h, "a-fixed-password"); err == nil {
		t.Fatal("the fixed password logged in")
	}
	st := h.Status()
	if st.Ask != "all" || !strings.Contains(strings.Join(st.FeaturesOff, ","), "shell") || !strings.Contains(strings.Join(st.FeaturesOff, ","), "file") ||
		strings.Contains(strings.Join(st.FeaturesOff, ","), "screen") {
		t.Fatalf("status: ask %q, off %v", st.Ask, st.FeaturesOff)
	}

	// Ask is forced although the config file says off; then Disconnect.
	go func() {
		deadline := time.Now().Add(10 * time.Second)
		for len(h.Pending()) == 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		h.Decide(0, true)
	}()
	conn, err := dialHost(t, h, h.Password())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Off in the config file stays off although the override allows it.
	if _, err := open(t, conn, svcShellish); err == nil {
		t.Fatal("a feature the config file switches off was allowed by an override")
	}
	if _, err := open(t, conn, svcScreen); err != nil {
		t.Fatalf("screen: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(h.Sessions()) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("no session on the host")
		}
		time.Sleep(5 * time.Millisecond)
	}
	id := h.Sessions()[0].ID()
	if id == 0 || h.Disconnect(id+1, "x") == nil {
		t.Fatalf("session ID %d; disconnecting an unknown session must fail", id)
	}
	if err := h.Disconnect(id, "disconnected at the host"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-conn.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("client still connected after Disconnect")
	}
}
