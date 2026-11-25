package nowindow

import (
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

func TestSetKeepsOtherFlags(t *testing.T) {
	cmd := exec.Command("cmd.exe", "/c", "exit 0")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	if Set(cmd) != cmd {
		t.Fatal("Set returns its argument")
	}
	f := cmd.SysProcAttr.CreationFlags
	if f&windows.CREATE_NO_WINDOW == 0 || f&syscall.CREATE_NEW_PROCESS_GROUP == 0 || !cmd.SysProcAttr.HideWindow {
		t.Fatalf("flags %#x hide %v", f, cmd.SysProcAttr.HideWindow)
	}
	if err := Set(exec.Command("cmd.exe", "/c", "exit 0")).Run(); err != nil {
		t.Fatal(err)
	}
}
