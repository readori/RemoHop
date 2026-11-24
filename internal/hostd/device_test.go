package hostd

import (
	"testing"
	"time"

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
