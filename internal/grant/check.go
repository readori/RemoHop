package grant

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
)

// Refusal is an action a grant does not allow. Its text is meant for the
// agent (or person) that attempted it: what was refused, under which grant,
// and why, in the allow-list vocabulary.
type Refusal struct {
	Grant string
	What  string // "files:write", "command \"rm -rf /\"", ...
	Why   string // optional detail
}

func (r *Refusal) Error() string {
	s := r.What + " is not allowed by grant " + r.Grant
	if r.Why != "" {
		s += " (" + r.Why + ")"
	}
	return s
}

// Is makes a refusal match fs.ErrPermission, so callers that only know
// about permission errors treat it as one.
func (r *Refusal) Is(target error) bool { return target == fs.ErrPermission }

// shellMeta are characters that let one command line start another, or
// change what runs, in POSIX sh or PowerShell. With a command allow-list
// they are refused outside quotes. alwaysMeta are refused everywhere: '$'
// and '`' expand inside double quotes, line breaks end a command, and the
// typographic quotes are quote characters to PowerShell (but not to sh),
// so they could make the two disagree about what is quoted.
const (
	shellMeta  = ";&|<>(){}"
	alwaysMeta = "$`\u0085\u2028\u2029\u2018\u2019\u201a\u201b\u201c\u201d\u201e\u201f"
)

// Dialect is the quoting dialect of the shell the host runs commands with.
type Dialect int

const (
	// DialectPOSIX: sh and its relatives (bash, dash, zsh, ksh, ...).
	DialectPOSIX Dialect = iota
	// DialectPowerShell: Windows PowerShell and PowerShell 7.
	DialectPowerShell
	// DialectCmd: cmd.exe, where only double quotes quote, and %, ^ and
	// ! expand or escape even inside them.
	DialectCmd
	// DialectUnknown: a shell whose rules the check does not know.
	DialectUnknown
)

// HostShell reports the dialect and path of the shell the host runs
// commands with. The shell service sets it; the default is POSIX.
var HostShell = func() (Dialect, string) { return DialectPOSIX, "" }

// cmdMeta are refused anywhere in a command for cmd.exe: % expands
// variables and ! (with delayed expansion) too, even inside double quotes,
// and ^ escapes the next character outside them.
const cmdMeta = "%^!"

// CheckCommand checks a command line against the allow-list, for the
// host's shell (HostShell). An empty list allows everything. An entry matches when the command's leading words are
// exactly the entry's words: "git" allows "git status" and "git log -1",
// "go test" allows "go test ./..." but not "go run". Commands that chain,
// redirect, substitute or expand anything are refused outright, because
// the leading words would no longer say what runs.
//
// This limits which programs the host starts on the agent's behalf. It is
// a guard rail, not a sandbox: a permitted program that can itself run
// others (git aliases and hooks, make, find -exec, interpreters) carries
// that power along. Allow such programs deliberately.
func CheckCommand(cmd string, allow []string) error {
	if len(allow) == 0 {
		return nil
	}
	dialect, sh := HostShell()
	return checkCommand(cmd, allow, dialect, sh)
}

func checkCommand(cmd string, allow []string, dialect Dialect, sh string) error {
	switch dialect {
	case DialectUnknown:
		return fmt.Errorf("the host runs commands with %s, whose quoting the allow-list does not know (it knows POSIX sh, PowerShell and cmd.exe; the host's owner can set REMOHOP_SHELL)", sh)
	case DialectCmd:
		if strings.ContainsAny(cmd, cmdMeta) {
			return errors.New("it contains %, ^ or !, which the allow-list does not permit when the host's shell is cmd.exe")
		}
	}
	if strings.ContainsAny(cmd, alwaysMeta) || strings.ContainsFunc(cmd, func(r rune) bool { return r < 0x20 && r != '\t' || r == 0x7f }) {
		return fmt.Errorf("it contains $, a backtick, a typographic quote, a line break or a control character, which the allow-list does not permit")
	}
	// A backslash before a quote is an escaped quote to sh but a literal
	// backslash (and a real quote) to PowerShell and to the split below.
	if strings.Contains(cmd, `\"`) || strings.Contains(cmd, `\'`) {
		return errors.New("it has a backslash before a quote, which the allow-list does not permit")
	}
	words, err := splitWords(cmd, dialect == DialectCmd)
	if err != nil {
		return err
	}
	if len(words) == 0 {
		return errors.New("empty command")
	}
	for _, a := range allow {
		want := strings.Fields(a)
		if len(want) > 0 && len(want) <= len(words) && slices.Equal(want, words[:len(want)]) {
			return nil
		}
	}
	return fmt.Errorf("allowed commands: %s", strings.Join(allow, "; "))
}

// splitWords splits a command line into words the way both POSIX sh and
// PowerShell would for the simple commands CheckCommand admits: blanks
// separate words, and single or double quotes group (without escapes).
// Shell operators outside quotes are an error. For cmd.exe (doubleOnly) a
// single quote is an ordinary character, as it is there.
func splitWords(cmd string, doubleOnly bool) ([]string, error) {
	var words []string
	var cur strings.Builder
	inWord := false
	var quote rune
	for _, c := range cmd {
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			} else {
				cur.WriteRune(c)
			}
		case c == '"' || c == '\'' && !doubleOnly:
			quote, inWord = c, true
		case c == ' ' || c == '\t':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		case strings.ContainsRune(shellMeta, c):
			return nil, fmt.Errorf("it uses the shell operator %q, which the allow-list does not permit (run one simple command per call)", c)
		default:
			cur.WriteRune(c)
			inWord = true
		}
	}
	if quote != 0 {
		return nil, errors.New("it has an unbalanced quote")
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words, nil
}

// File access kinds.
type Access int

const (
	// Meta is reading attributes (stat, realpath): allowed on the roots,
	// below them and on their ancestors, so a client can find its way to
	// a root.
	Meta Access = iota
	// Read is reading contents or listing a directory.
	Read
	// Write is creating, changing, renaming or removing.
	Write
)

func (a Access) String() string {
	switch a {
	case Meta, Read:
		return "files:read"
	}
	return "files:write"
}

// CheckWorkDir decides whether a command may run in host directory dir
// (absolute) under the rules. Without a command allow-list anything goes:
// such a grant runs whatever it likes anyway. With one, the directory may
// not be at or below a root the grant may write, nor contain one: the
// allowed programs read files there that decide what they run (git reads
// .git/config and its hooks, go test compiles every package below it, make
// reads its Makefile, npm its package.json), so a session that may write
// there could make an allowed command run a program of its own. A grant
// that may write anywhere (files) can therefore run its commands nowhere.
func (r *Rules) CheckWorkDir(dir string) error {
	if len(r.Commands) == 0 {
		return nil
	}
	if r.WriteAll {
		return errors.New("the grant may change files anywhere (files), so with a command allow-list no working directory is safe: " +
			"an allowed program could run what the session wrote; give write access to a narrower root (files:write:PATH) outside the directories the commands work in")
	}
	if len(r.WriteRoots) == 0 {
		return nil
	}
	if !filepath.IsAbs(dir) {
		return fmt.Errorf("working directory %s is not an absolute path", dir)
	}
	dir = filepath.Clean(dir)
	rd, err := RealPath(dir)
	if err != nil {
		return err
	}
	for _, root := range r.WriteRoots {
		rr, err := RealPath(root)
		if err != nil {
			rr = root
		}
		switch {
		case withinFold(dir, root) || withinFold(rd, rr):
			return fmt.Errorf("the working directory %s is in the writable root %s; with a command allow-list commands may not run where the session can write "+
				"(an allowed program could run what it wrote there): run them in a directory outside the writable roots", dir, root)
		case withinFold(root, dir) || withinFold(rr, rd):
			return fmt.Errorf("the working directory %s contains the writable root %s; with a command allow-list commands may not run above files the session can write "+
				"(an allowed program could run what it wrote there): run them in a directory that holds no writable root", dir, root)
		}
	}
	return nil
}
