package desktop

import (
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// All encoder processes are put into one job object that kills its members
// when its last handle closes, i.e. when the host process exits for any
// reason. Not verified on real Windows.
var (
	jobOnce sync.Once
	job     windows.Handle
)

func killOnExitJob() windows.Handle {
	jobOnce.Do(func() {
		h, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(h, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			windows.CloseHandle(h)
			return
		}
		job = h
	})
	return job
}

func startGroup(cmd *exec.Cmd) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	if j := killOnExitJob(); j != 0 {
		if p, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid)); err == nil {
			windows.AssignProcessToJobObject(j, p)
			windows.CloseHandle(p)
		}
	}
	return nil
}

func killGroup(cmd *exec.Cmd) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}
