package unattended

import (
	"errors"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

var (
	sasDLL      = windows.NewLazySystemDLL("sas.dll")
	procSendSAS = sasDLL.NewProc("SendSAS")
)

func sasPolicy() (uint32, bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows\CurrentVersion\Policies\System`, registry.QUERY_VALUE)
	if err != nil {
		return 0, false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("SoftwareSASGeneration")
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

// SASPolicyAllows reports whether the SoftwareSASGeneration policy lets
// services raise Ctrl+Alt+Del.
func SASPolicyAllows() bool { return sasAllowed(sasPolicy()) }

// SASAvailable reports whether this process can raise Ctrl+Alt+Del, and if
// not, why.
func SASAvailable() (bool, string) {
	if !Active() {
		return false, "only the RemoHop service with unattended access can send Ctrl+Alt+Del (remohop service install --unattended)"
	}
	if err := procSendSAS.Find(); err != nil {
		return false, "sas.dll is not available on this edition of Windows"
	}
	if !sasAllowed(sasPolicy()) {
		return false, SASPolicyHint
	}
	return true, ""
}

// SendSAS raises the secure attention sequence (Ctrl+Alt+Del) on the console
// session. Windows does it for a service only when the SoftwareSASGeneration
// policy allows services; SendSAS itself reports nothing.
func SendSAS() error {
	if ok, why := SASAvailable(); !ok {
		return errors.New(why)
	}
	procSendSAS.Call(0) // AsUser = FALSE: the caller is a service
	return nil
}
