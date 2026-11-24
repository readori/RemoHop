package unattended

import (
	"bufio"
	"cmp"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// helperProc is a running helper, as the service sees it.
type helperProc struct {
	mu      sync.Mutex // orders kill with closing proc
	session uint32
	pid     uint32
	proc    windows.Handle
	pipe    string
	secret  []byte
	exited  chan struct{}
}

func (p *helperProc) alive() bool {
	select {
	case <-p.exited:
		return false
	default:
		return true
	}
}

// kill ends the helper.
func (p *helperProc) kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.alive() {
		windows.TerminateProcess(p.proc, 1)
	}
}

// newKillJob makes a job object whose processes die when the service does
// (its handle closes with the process).
func newKillJob() (windows.Handle, error) {
	j, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(j, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(j)
		return 0, err
	}
	return j, nil
}

// launchHelper starts `remohop service session-helper` in session as
// LocalSystem, on winsta0\Winlogon (a desktop every interactive session has;
// the helper's threads move to the input desktop by themselves), and hands
// it its configuration through its standard input. log receives what the
// helper prints.
func launchHelper(session uint32, job windows.Handle, log io.Writer) (*helperProc, error) {
	cfg, err := newHelperConfig(windows.GetCurrentProcessId(), session)
	if err != nil {
		return nil, err
	}
	secret, _ := hex.DecodeString(cfg.Secret)
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}

	// The service's own token (LocalSystem), moved to the session. Setting
	// TokenSessionId needs SeTcbPrivilege, which LocalSystem holds.
	var self windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY|windows.TOKEN_ADJUST_PRIVILEGES, &self); err != nil {
		return nil, fmt.Errorf("OpenProcessToken: %w", err)
	}
	defer self.Close()
	// LocalSystem holds these; make sure they are enabled for
	// SetTokenInformation(TokenSessionId) and CreateProcessAsUser.
	if err := enablePrivileges(self, "SeTcbPrivilege", "SeAssignPrimaryTokenPrivilege", "SeIncreaseQuotaPrivilege"); err != nil {
		fmt.Fprintf(log, "unattended: enabling privileges: %v\n", err)
	}
	var tok windows.Token
	if err := windows.DuplicateTokenEx(self, windows.MAXIMUM_ALLOWED, nil, windows.SecurityIdentification, windows.TokenPrimary, &tok); err != nil {
		return nil, fmt.Errorf("DuplicateTokenEx: %w", err)
	}
	defer tok.Close()
	sid := session
	if err := windows.SetTokenInformation(tok, windows.TokenSessionId, (*byte)(unsafe.Pointer(&sid)), uint32(unsafe.Sizeof(sid))); err != nil {
		return nil, fmt.Errorf("moving the token to session %d: %w (the service must run as LocalSystem)", session, err)
	}

	// Standard input carries the configuration; standard output and error
	// come back to the service's log. Only these two handles are
	// inherited (PROC_THREAD_ATTRIBUTE_HANDLE_LIST).
	inheritable := &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	var inR, inW, outR, outW windows.Handle
	if err := windows.CreatePipe(&inR, &inW, inheritable, 0); err != nil {
		return nil, err
	}
	if err := windows.CreatePipe(&outR, &outW, inheritable, 0); err != nil {
		windows.CloseHandle(inR)
		windows.CloseHandle(inW)
		return nil, err
	}
	closeAll := func(hs ...windows.Handle) {
		for _, h := range hs {
			if h != 0 {
				windows.CloseHandle(h)
			}
		}
	}
	windows.SetHandleInformation(inW, windows.HANDLE_FLAG_INHERIT, 0)
	windows.SetHandleInformation(outR, windows.HANDLE_FLAG_INHERIT, 0)

	attrs, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		closeAll(inR, inW, outR, outW)
		return nil, err
	}
	defer attrs.Delete()
	handles := []windows.Handle{inR, outW}
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
		closeAll(inR, inW, outR, outW)
		return nil, err
	}
	si := &windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	si.Cb = uint32(unsafe.Sizeof(*si))
	si.Flags = windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW
	si.ShowWindow = windows.SW_HIDE
	si.StdInput, si.StdOutput, si.StdErr = inR, outW, outW
	si.Desktop, _ = windows.UTF16PtrFromString(`winsta0\Winlogon`)
	exeW, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		closeAll(inR, inW, outR, outW)
		return nil, err
	}
	cmdW, err := windows.UTF16PtrFromString(windows.ComposeCommandLine([]string{exe, "service", "session-helper", "--session", strconv.FormatUint(uint64(session), 10)}))
	if err != nil {
		closeAll(inR, inW, outR, outW)
		return nil, err
	}
	var pi windows.ProcessInformation
	// os/exec also creates inheritable handles under ForkLock.
	syscall.ForkLock.Lock()
	err = windows.CreateProcessAsUser(tok, exeW, cmdW, nil, nil, true,
		windows.CREATE_NO_WINDOW|windows.CREATE_SUSPENDED|windows.EXTENDED_STARTUPINFO_PRESENT|windows.CREATE_UNICODE_ENVIRONMENT,
		nil, nil, &si.StartupInfo, &pi)
	syscall.ForkLock.Unlock()
	closeAll(inR, outW)
	if err != nil {
		closeAll(inW, outR)
		return nil, fmt.Errorf("starting the session helper in session %d: %w", session, err)
	}
	if job != 0 {
		if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
			fmt.Fprintf(log, "unattended: cannot tie the helper to the service's lifetime: %v\n", err)
		}
	}
	windows.ResumeThread(pi.Thread)
	windows.CloseHandle(pi.Thread)

	p := &helperProc{session: session, pid: pi.ProcessId, proc: pi.Process, pipe: cfg.Pipe, secret: secret, exited: make(chan struct{})}
	go func() {
		windows.WaitForSingleObject(pi.Process, windows.INFINITE)
		var code uint32
		windows.GetExitCodeProcess(pi.Process, &code)
		fmt.Fprintf(log, "unattended: session helper (session %d, pid %d) exited with status %d\n", session, pi.ProcessId, code)
		p.mu.Lock()
		windows.CloseHandle(pi.Process)
		close(p.exited)
		p.mu.Unlock()
	}()
	go func() {
		f := os.NewFile(uintptr(outR), "helper-output")
		defer f.Close()
		sc := bufio.NewScanner(io.LimitReader(f, 64<<20))
		for sc.Scan() {
			fmt.Fprintf(log, "unattended: session helper (session %d): %s\n", session, sanitizeLine(sc.Text()))
		}
	}()
	line, _ := json.Marshal(cfg)
	in := os.NewFile(uintptr(inW), "helper-input")
	_, err = in.Write(append(line, '\n'))
	in.Close()
	if err != nil {
		p.kill()
		return nil, fmt.Errorf("handing the session helper its configuration: %w", err)
	}
	return p, nil
}

// sanitizeLine keeps a helper's log line printable and short.
func sanitizeLine(s string) string {
	if len(s) > 2000 {
		s = s[:2000]
	}
	b := []rune(s)
	for i, r := range b {
		if r < 0x20 || r == 0x7f {
			b[i] = '?'
		}
	}
	return string(b)
}

// enablePrivileges enables privileges the token holds.
func enablePrivileges(tok windows.Token, names ...string) error {
	var firstErr error
	for _, name := range names {
		var luid windows.LUID
		p, _ := windows.UTF16PtrFromString(name)
		if err := windows.LookupPrivilegeValue(nil, p, &luid); err != nil {
			firstErr = cmp.Or(firstErr, fmt.Errorf("%s: %w", name, err))
			continue
		}
		tp := windows.Tokenprivileges{PrivilegeCount: 1}
		tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
		if err := windows.AdjustTokenPrivileges(tok, false, &tp, 0, nil, nil); err != nil {
			firstErr = cmp.Or(firstErr, fmt.Errorf("%s: %w", name, err))
		}
	}
	return firstErr
}
