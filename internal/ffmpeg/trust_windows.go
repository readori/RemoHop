//go:build windows

package ffmpeg

import (
	"path/filepath"

	"golang.org/x/sys/windows"

	"github.com/readori/RemoHop/internal/config"
)

// trustedCopy reports whether this process may run RemoHop's copy at p.
//
// It matters for the Windows service, which runs as LocalSystem: whoever
// can replace the program it runs gets LocalSystem. So the service runs
// its copy (in its own directory, %ProgramData%\RemoHop\ffmpeg) only when
// nobody but SYSTEM and the Administrators can change it: the program, its
// directory and the directory above are not links or junctions, belong to
// SYSTEM or the Administrators (an owner may rewrite the access list), and
// their access lists let nobody else in (config.IsPrivate, which for
// LocalSystem admits just those two). InstallFor, through the service's
// Secure, leaves the copy so. Anything else, such as a directory put there
// while someone else could write the state directory, is not run.
//
// A user's own copy, in that user's directory, is that user's business.
func trustedCopy(p string) bool {
	if !runningAsSystem() {
		return true
	}
	for _, x := range []string{p, filepath.Dir(p), filepath.Dir(filepath.Dir(p))} {
		if !adminOnly(x) {
			return false
		}
	}
	return true
}

func runningAsSystem() bool {
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	return err == nil && u.User.Sid.IsWellKnown(windows.WinLocalSystemSid)
}

// adminOnly reports whether path, a file or directory that is not a link,
// can be changed by SYSTEM and the Administrators only.
func adminOnly(path string) bool {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil || attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return false
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	owner, _, err := sd.Owner()
	if err != nil || owner == nil ||
		!(owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid)) {
		return false
	}
	ok, err := config.IsPrivate(path)
	return err == nil && ok
}
