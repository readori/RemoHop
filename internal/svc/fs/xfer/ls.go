package xfer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

func init() {
	cli.Register(&cli.Command{
		Name:    "ls",
		Summary: "list a remote directory",
		Remote:  true,
		Usage: `Usage: remohop <ID> ls [-l] [-a] [--json] [:path]

List a directory on the remote machine: the remote home by default, or
:path (absolute, or relative to the remote home; the leading ':' is
optional). Without -l, names only, one per line, directories marked with a
trailing '/'. With -l, also the mode, size in bytes and modification time,
and the target of symlinks. Entries starting with '.' are shown with -a. A
path naming a file lists just that file. Names are printed with control
characters replaced; --json carries them exactly (escaped).

Takes one round trip per 1024 entries.

Flags:
  -l       long format: mode, size (bytes), mtime, name [-> target]
  -a       include entries whose names start with '.'
  --json   JSON array of {name, type, size, mode, mtime, target}
           (type: file, dir, symlink, pipe, socket, device, chardevice;
           mode: octal permission bits; mtime: RFC 3339)

Examples:
  remohop 123456789 ls
  remohop 123456789 ls -l :/var/log
  remohop 123456789 ls --json :src`,
		Run: runLs,
	})
}

// LsEntry is one entry of `ls --json`.
type LsEntry struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
	Mtime  string `json:"mtime"`
	Target string `json:"target,omitempty"`
}

func fileType(m fs.FileMode) string {
	switch {
	case m.IsDir():
		return "dir"
	case m&fs.ModeSymlink != 0:
		return "symlink"
	case m&fs.ModeNamedPipe != 0:
		return "pipe"
	case m&fs.ModeSocket != 0:
		return "socket"
	case m&fs.ModeCharDevice != 0:
		return "chardevice"
	case m&fs.ModeDevice != 0:
		return "device"
	}
	return "file"
}

// modeString renders a mode the way ls -l does ("drwxr-sr-x", "lrwxrwxrwx").
func modeString(m fs.FileMode) string {
	b := []byte("----------")
	switch fileType(m) {
	case "dir":
		b[0] = 'd'
	case "symlink":
		b[0] = 'l'
	case "pipe":
		b[0] = 'p'
	case "socket":
		b[0] = 's'
	case "chardevice":
		b[0] = 'c'
	case "device":
		b[0] = 'b'
	}
	const rwx = "rwxrwxrwx"
	for i := 0; i < 9; i++ {
		if m&(1<<uint(8-i)) != 0 {
			b[1+i] = rwx[i]
		}
	}
	special := func(i int, bit fs.FileMode, set, unset byte) {
		if m&bit != 0 {
			if b[i] == '-' {
				b[i] = unset
			} else {
				b[i] = set
			}
		}
	}
	special(3, fs.ModeSetuid, 's', 'S')
	special(6, fs.ModeSetgid, 's', 'S')
	special(9, fs.ModeSticky, 't', 'T')
	return string(b)
}

func runLs(ctx context.Context, env *cli.Env, args []string) int {
	fset := cli.NewFlagSet(env, "ls", "")
	long := fset.Bool("l", false, "long format")
	all := fset.Bool("a", false, "show dot files")
	asJSON := fset.Bool("json", false, "JSON output")
	pos, err := parseArgs(fset, args)
	if err != nil {
		return cli.FlagExit(err)
	}
	if len(pos) > 1 {
		env.Errorf("ls: one path at most (see `remohop help ls`)")
		return cli.ExitUsage
	}
	p := ""
	if len(pos) == 1 {
		p = strings.TrimPrefix(pos[0], ":")
	}
	c, code := connect(ctx, env)
	if c == nil {
		return code
	}
	defer c.Close()

	ents, err := c.ReadDir(ctx, p)
	single := false // a path naming a file lists that file, dot or not
	if errors.Is(err, syscall.ENOTDIR) {
		var fi *rfs.FileInfo
		if fi, err = c.Lstat(ctx, p); err == nil {
			ents, single = []*rfs.FileInfo{fi}, true
		}
	}
	if err != nil {
		env.Errorf("ls: %v", err)
		return cli.ExitError
	}
	out := []LsEntry{}
	for _, fi := range ents {
		if !*all && strings.HasPrefix(fi.Name(), ".") && !single {
			continue
		}
		out = append(out, LsEntry{
			Name: fi.Name(), Type: fileType(fi.Mode()), Size: fi.Size(),
			Mode:  fmt.Sprintf("%04o", rfs.ModeToUnix(fi.Mode())&0o7777),
			Mtime: fi.ModTime().UTC().Format(time.RFC3339), Target: fi.Target(),
		})
		if !*asJSON {
			name := cli.Sanitize(fi.Name())
			if fi.IsDir() {
				name += "/"
			}
			if !*long {
				fmt.Fprintln(env.Stdout, name)
				continue
			}
			if fi.IsSymlink() {
				name += " -> " + cli.Sanitize(fi.Target())
			}
			fmt.Fprintf(env.Stdout, "%s %12d %s %s\n", modeString(fi.Mode()), fi.Size(),
				fi.ModTime().Local().Format("2006-01-02 15:04"), name)
		}
	}
	if *asJSON {
		printJSON(env.Stdout, out)
	}
	return cli.ExitOK
}
