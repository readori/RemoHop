package vpn

import (
	"fmt"
	"regexp"
)

// Changes that are undone by something other than a command line from
// allowedUndo: deleting one known file, removing Windows name resolution
// policy (NRPT) rules by their comment. Both are as narrow as the command
// allow-list: a state file can name nothing else.

// removableFiles are the only files an undo step may delete.
var removableFiles = map[string]bool{
	"/etc/resolver/remohop": true, // macOS split DNS of `remohop net`
}

var nrptCommentRE = regexp.MustCompile(`^remohop-[a-z0-9-]{1,32}$`)

func validNRPTComment(c string) bool { return nrptCommentRE.MatchString(c) }

// RecordRemove records that path is to be deleted on undo. Only the
// files in removableFiles can be.
func (c *Changes) RecordRemove(path string) error {
	if !removableFiles[path] {
		return fmt.Errorf("%s is not a file an undo may remove", path)
	}
	return c.st.record(undoStep{Remove: path})
}

// RecordNRPTRemoval records that the NRPT rules with this comment are to
// be removed on undo (Windows).
func (c *Changes) RecordNRPTRemoval(comment string) error {
	if !validNRPTComment(comment) {
		return fmt.Errorf("%q is not a RemoHop NRPT rule comment", comment)
	}
	return c.st.record(undoStep{NRPT: comment})
}

// PowerShell runs a short script without a profile or prompts.
func PowerShell(script string) []string {
	return []string{"powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", script}
}

// NRPTRemoveCmd removes the NRPT rules carrying comment and forgets
// cached answers. comment must pass validNRPTComment (it is quoted into
// the script).
func NRPTRemoveCmd(comment string) []string {
	if !validNRPTComment(comment) {
		comment = "remohop-invalid"
	}
	return PowerShell(fmt.Sprintf("Get-DnsClientNrptRule | Where-Object { $_.Comment -eq '%s' } | Remove-DnsClientNrptRule -Force; "+
		"Clear-DnsClientCache", comment))
}
