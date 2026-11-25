//go:build windows

package service

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
)

// The service runs as LocalSystem. Two things it uses must therefore be out
// of reach of other accounts: the binary it starts (otherwise replacing it
// gives LocalSystem), and its state directory (otherwise planted keys,
// passwords or authorised keys let someone log in to a SYSTEM host).

// serviceExeDir is where `service install` keeps the service's copy of the
// binary: under Program Files, which only administrators can change.
func serviceExeDir() string {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	return filepath.Join(pf, "RemoHop")
}

func serviceExeName(svcName string) string {
	switch l := strings.ToLower(svcName); {
	case l == "remohop":
		return "remohop.exe"
	case strings.HasPrefix(l, "remohop"):
		return svcName + ".exe"
	}
	return "remohop-" + svcName + ".exe"
}

// installBinary returns the binary the service should run: src itself when
// it already lies where only administrators can write, else a copy of it in
// serviceExeDir (replacing the previous copy, which may still be running).
func installBinary(src, svcName string) (exe string, copied bool, err error) {
	if adminOnlyLocation(src) {
		return src, false, nil
	}
	dir := serviceExeDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, err
	}
	dst := filepath.Join(dir, serviceExeName(svcName))
	if a, err1 := os.Stat(src); err1 == nil {
		if b, err2 := os.Stat(dst); err2 == nil && os.SameFile(a, b) {
			return dst, false, nil
		}
	}
	tmp := dst + ".new-" + randSuffix()
	if err := copyFile(src, tmp); err != nil {
		os.Remove(tmp)
		return "", false, fmt.Errorf("cannot copy %s to %s: %w", src, dir, err)
	}
	if _, err := os.Lstat(dst); err == nil {
		// A running image cannot be overwritten, but it can be renamed.
		if err := os.Rename(dst, dst+".old-"+randSuffix()); err != nil {
			os.Remove(tmp)
			return "", false, err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		os.Remove(tmp)
		return "", false, err
	}
	removeOldCopies(dst)
	return dst, true, nil
}

// removeOldCopies deletes the copies replaced earlier that are no longer
// in use.
func removeOldCopies(dst string) {
	old, _ := filepath.Glob(dst + ".old-*")
	for _, f := range old {
		os.Remove(f)
	}
}

// removeInstalledBinary deletes the service's copy of the binary after the
// service is gone, if it is one that installBinary made.
func removeInstalledBinary(exe string) {
	dir := serviceExeDir()
	if !strings.EqualFold(filepath.Dir(exe), dir) {
		return
	}
	base := strings.ToLower(filepath.Base(exe))
	if !strings.HasPrefix(base, "remohop") || !strings.HasSuffix(base, ".exe") {
		return
	}
	for i := 0; i < 10; i++ { // the process may take a moment to go away
		if err := os.Remove(exe); err == nil || errors.Is(err, fs.ErrNotExist) {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	removeOldCopies(exe)
	os.Remove(dir) // only if empty
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func randSuffix() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// commandLine is the service's command line, quoted as CreateService does.
func commandLine(exe string, args []string) string {
	s := windows.EscapeArg(exe)
	for _, a := range args {
		s += " " + windows.EscapeArg(a)
	}
	return s
}

// Owners whose state directory the service accepts.
var trustedOwnerSIDs = func() []*windows.SID {
	var sids []*windows.SID
	for _, t := range []windows.WELL_KNOWN_SID_TYPE{windows.WinLocalSystemSid, windows.WinBuiltinAdministratorsSid} {
		if s, err := windows.CreateWellKnownSid(t); err == nil {
			sids = append(sids, s)
		}
	}
	if s, err := windows.StringToSid("S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"); err == nil {
		sids = append(sids, s) // TrustedInstaller
	}
	return sids
}()

func trustedOwner(sid *windows.SID, extra *windows.SID) bool {
	if sid == nil {
		return false
	}
	if extra != nil && sid.Equals(extra) {
		return true
	}
	for _, t := range trustedOwnerSIDs {
		if sid.Equals(t) {
			return true
		}
	}
	return false
}

func accountName(sid *windows.SID) string {
	if sid == nil {
		return "nobody"
	}
	if a, d, _, err := sid.LookupAccount(""); err == nil {
		if d != "" {
			return d + `\` + a
		}
		return a
	}
	return sid.String()
}

// openNoFollow opens path itself (not what a link or junction points at).
func openNoFollow(path string, access uint32) (windows.Handle, *windows.ByHandleFileInformation, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, nil, err
	}
	h, err := windows.CreateFile(p, access,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return 0, nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return 0, nil, err
	}
	return h, &info, nil
}

func ownerOf(h windows.Handle) (*windows.SID, error) {
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	o, _, err := sd.Owner()
	return o, err
}

var (
	adminsSID, _ = windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	// SYSTEM and Administrators only, not inherited from above.
	dirSDDL  = "D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	fileSDDL = "D:PAI(A;;FA;;;SY)(A;;FA;;;BA)"
)

func dacl(sddl string) (*windows.ACL, error) {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, err
	}
	d, _, err := sd.DACL()
	return d, err
}

// secureDir makes the Administrators the owner of dir and replaces its
// access list with one that admits only SYSTEM and the Administrators,
// not inherited from the parent and passed on to what is inside.
// %ProgramData% lets every local user read what is created under it, and
// this directory holds the identity key and the host password.
func secureDir(dir string) error {
	d, err := dacl(dirSDDL)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		adminsSID, nil, d, nil)
}

// prepareStateDir runs at `service install`, as the administrator who
// installs. Any local user may create directories under %ProgramData%, so
// one that already exists may have been put there to make the service use
// planted credentials. It is accepted only if it and everything in it
// belongs to SYSTEM, the Administrators, TrustedInstaller or the installing
// administrator, and contains no links, junctions or hard links; then the
// Administrators become the owner of all of it, and only SYSTEM and the
// Administrators may access it.
func prepareStateDir(dir string) error {
	me, err := currentUserSID()
	if err != nil {
		return err
	}
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		return secureDir(dir)
	}
	refuse := func(why string) error {
		return fmt.Errorf("the state directory %s already exists, and %s. It may hold credentials someone else put there for the service to use. "+
			"Move it aside or delete it, then install again. (If you made it yourself, from an elevated prompt: "+
			"takeown /a /r /d y /f \"%s\", then install again.)", dir, why, dir)
	}
	var walk func(p string, isRoot bool) error
	walk = func(p string, isRoot bool) error {
		const access = windows.READ_CONTROL | windows.WRITE_DAC | windows.WRITE_OWNER
		h, info, err := openNoFollow(p, access)
		if err != nil {
			return refuse(fmt.Sprintf("%s cannot be opened by an administrator (%v)", p, err))
		}
		defer windows.CloseHandle(h)
		isDir := info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0
		switch {
		case info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0:
			return refuse(p + " is a link or junction")
		case isRoot && !isDir:
			return refuse("it is not a directory")
		case !isDir && info.NumberOfLinks > 1:
			return refuse(p + " has other names (hard links)")
		}
		owner, err := ownerOf(h)
		if err != nil {
			return err
		}
		if !trustedOwner(owner, me) {
			return refuse(fmt.Sprintf("%s belongs to %s", p, accountName(owner)))
		}
		if isDir {
			entries, err := os.ReadDir(p)
			if err != nil {
				return err
			}
			for _, e := range entries {
				if err := walk(filepath.Join(p, e.Name()), false); err != nil {
					return err
				}
			}
		}
		sddl := fileSDDL
		if isDir {
			sddl = dirSDDL
		}
		d, err := dacl(sddl)
		if err != nil {
			return err
		}
		return windows.SetSecurityInfo(h, windows.SE_FILE_OBJECT,
			windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
			adminsSID, nil, d, nil)
	}
	return walk(dir, true)
}

func currentUserSID() (*windows.SID, error) {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return u.User.Sid.Copy()
}

// adoptStateDir runs each time the service starts, as LocalSystem, before
// the host reads anything there. A directory that is not SYSTEM's or the
// Administrators' (or is a link) was not prepared by `service install`: it
// is moved aside, kept for inspection, and the service starts with a new,
// empty one. note says what happened, for the log.
func adoptStateDir(dir string) (note string, err error) {
	if _, err := os.Lstat(dir); err == nil {
		bad := ""
		h, info, err := openNoFollow(dir, windows.READ_CONTROL)
		if err != nil {
			bad = err.Error()
		} else {
			owner, oerr := ownerOf(h)
			windows.CloseHandle(h)
			switch {
			case info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0:
				bad = "it is a link or junction"
			case info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0:
				bad = "it is not a directory"
			case oerr != nil:
				bad = oerr.Error()
			case !trustedOwner(owner, nil):
				bad = "it belongs to " + accountName(owner)
			}
		}
		if bad != "" {
			aside := fmt.Sprintf("%s.untrusted-%d", dir, time.Now().Unix())
			if err := os.Rename(dir, aside); err != nil {
				return "", fmt.Errorf("%s cannot be trusted (%s) and cannot be moved aside: %w", dir, bad, err)
			}
			note = fmt.Sprintf("remohop: the state directory %s could not be trusted (%s); it was moved to %s and this service starts with a new identity", dir, bad, aside)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return note, err
	}
	if err := secureDir(dir); err != nil {
		return note, fmt.Errorf("cannot restrict access to %s: %w", dir, err)
	}
	return note, nil
}
