package hostd

import (
	"os"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/svc/shell"
)

func TestApplyTermKeep(t *testing.T) {
	old := shell.KeepAlive
	defer func() { shell.KeepAlive = old }()
	t.Setenv("REMOHOP_SHELL_KEEPALIVE", "")
	applyTermKeep(map[string]string{SettingTermKeep: "2h"})
	if shell.KeepAlive != 2*time.Hour {
		t.Fatalf("KeepAlive %s, want 2h", shell.KeepAlive)
	}
	applyTermKeep(map[string]string{SettingTermKeep: "garbage"})
	if shell.KeepAlive != 2*time.Hour {
		t.Fatal("an unparsable setting changed the keep-alive")
	}
	// The environment, which the shell package already applied, wins.
	t.Setenv("REMOHOP_SHELL_KEEPALIVE", "5m")
	applyTermKeep(map[string]string{SettingTermKeep: "9h"})
	if shell.KeepAlive != 2*time.Hour {
		t.Fatal("the config file overrode REMOHOP_SHELL_KEEPALIVE")
	}
}

// TestDeviceNameDefault: without a "name" setting the device goes by the
// computer's host name, so an approval prompt never shows an empty name.
func TestDeviceNameDefault(t *testing.T) {
	t.Setenv("REMOHOP_CONFIG_DIR", t.TempDir())
	h, err := os.Hostname()
	if err != nil || h == "" {
		t.Skip("no host name here")
	}
	if got := DeviceName(); got != auth.CleanText(h, auth.MaxTokenName) {
		t.Errorf("default name %q, host name %q", got, h)
	}
	if got := ConfiguredDeviceName(); got != "" {
		t.Errorf("configured name %q without a setting", got)
	}
	if err := config.Set(SettingName, "work laptop"); err != nil {
		t.Fatal(err)
	}
	if got := DeviceName(); got != "work laptop" {
		t.Errorf("set name: %q", got)
	}
}
