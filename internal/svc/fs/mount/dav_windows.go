package mount

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/readori/RemoHop/internal/nowindow"
)

// davTarget validates a Windows mount target: a drive letter ("Z:") or "*"
// for the last free one.
func davTarget(local string) (string, error) {
	if local == "*" {
		return "*", nil
	}
	t := strings.ToUpper(strings.TrimRight(local, `\/`))
	if len(t) != 2 || t[1] != ':' || t[0] < 'A' || t[0] > 'Z' {
		return "", fmt.Errorf("%q: on Windows the mount target is a drive letter such as Z: or * for a free one", local)
	}
	if davMounted(t) {
		return "", fmt.Errorf("drive %s is in use", t)
	}
	return t, nil
}

func davMounted(target string) bool {
	if len(target) != 2 {
		return false
	}
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		_, serr := os.Stat(target + `\`)
		return serr == nil
	}
	return mask&(1<<(target[0]-'A')) != 0
}

func freeDrive() (string, error) {
	mask, err := windows.GetLogicalDrives()
	if err != nil {
		return "", err
	}
	for l := byte('Z'); l >= 'D'; l-- {
		if mask&(1<<(l-'A')) == 0 {
			return string(l) + ":", nil
		}
	}
	return "", errors.New("no free drive letter")
}

// webClientCheck makes sure the WebClient service (the WebDAV redirector)
// can be used, starting it if it is merely stopped.
func webClientCheck() error {
	out, err := nowindow.Set(exec.Command("sc.exe", "query", "WebClient")).CombinedOutput()
	if err != nil {
		// 1060: the service does not exist (Windows Server without the
		// WebDAV Redirector feature).
		return errors.New("the WebClient service (Windows' WebDAV redirector) is not installed. On Windows Server, install it from an elevated PowerShell with `Install-WindowsFeature WebDAV-Redirector` and restart; desktop editions of Windows include it")
	}
	if strings.Contains(string(out), "RUNNING") {
		return nil
	}
	if qc, _ := nowindow.Set(exec.Command("sc.exe", "qc", "WebClient")).CombinedOutput(); strings.Contains(string(qc), "DISABLED") {
		return errors.New("the WebClient service (Windows' WebDAV redirector) is disabled. Enable it from an elevated prompt with `sc config WebClient start= demand`, then `net start WebClient`")
	}
	// Stopped but startable: `net use` triggers it for ordinary users;
	// starting it here covers elevated shells where the trigger is slow.
	nowindow.Set(exec.Command("sc.exe", "start", "WebClient")).Run()
	for i := 0; i < 20 && !webClientRunning(); i++ {
		time.Sleep(250 * time.Millisecond)
	}
	return nil
}

func webClientRunning() bool {
	out, err := nowindow.Set(exec.Command("sc.exe", "query", "WebClient")).CombinedOutput()
	return err == nil && strings.Contains(string(out), "RUNNING")
}

// fileSizeLimit is the redirector's FileSizeLimitInBytes: it refuses to
// read files larger than this (50 MB unless configured).
func fileSizeLimit() uint64 {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\WebClient\Parameters`, registry.QUERY_VALUE)
	if err != nil {
		return 0
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("FileSizeLimitInBytes")
	if err != nil {
		return 0
	}
	return v
}

// davMount maps a drive to the server through the WebClient redirector:
//
//	net use Z: \\127.0.0.1@PORT\DavWWWRoot\<secret> /persistent:no
//
// No user name or password is involved: Windows refuses Basic
// authentication over plain HTTP by default, so the unguessable path is
// the credential.
func davMount(url string, port int, secret, target string, o Options) (string, error) {
	if err := webClientCheck(); err != nil {
		return "", err
	}
	if target == "*" {
		t, err := freeDrive()
		if err != nil {
			return "", err
		}
		target = t
	}
	unc := fmt.Sprintf(`\\127.0.0.1@%d\DavWWWRoot\%s`, port, secret)
	out, err := nowindow.Set(exec.Command("net.exe", "use", target, unc, "/persistent:no")).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("net use %s: %v: %s (is the WebClient service running? `sc query WebClient`)", target, err, strings.TrimSpace(string(out)))
	}
	if lim := fileSizeLimit(); lim > 0 && o.Logf != nil {
		o.Logf("note: Windows' WebDAV redirector will not open files larger than %d MB through this drive (FileSizeLimitInBytes under HKLM\\SYSTEM\\CurrentControlSet\\Services\\WebClient\\Parameters; use `remohop cp` for bigger files)", lim>>20)
	}
	return target, nil
}

func davUnmount(target string, force bool) error {
	out, err := nowindow.Set(exec.Command("net.exe", "use", target, "/delete", "/y")).CombinedOutput()
	if err != nil && davMounted(target) {
		return fmt.Errorf("net use %s /delete: %v: %s", target, err, strings.TrimSpace(string(out)))
	}
	return nil
}
