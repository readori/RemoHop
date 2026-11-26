package desktop

import (
	"fmt"
	"os/exec"

	"golang.org/x/sys/windows"

	"github.com/readori/RemoHop/internal/nowindow"
)

func osVersion() string {
	v := windows.RtlGetVersion()
	return fmt.Sprintf("Windows %d.%d build %d", v.MajorVersion, v.MinorVersion, v.BuildNumber)
}

// revealFile opens Explorer with the file selected.
func revealFile(path string) error {
	cmd := exec.Command("explorer.exe", "/select,"+path)
	if err := nowindow.Set(cmd).Start(); err != nil {
		return err
	}
	go cmd.Wait() // explorer exits with 1 even when it worked
	return nil
}
