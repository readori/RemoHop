// Package mount implements `remohop <ID> mount` and `remohop umount`: a directory
// of the remote machine shown as a local directory (or drive), so local
// editors, IDEs and tools work on remote code directly. It runs on the rfs
// protocol of the fs service, in two ways:
//
//   - FUSE (github.com/hanwen/go-fuse/v2), the default on Linux;
//   - a loopback WebDAV server (golang.org/x/net/webdav) mounted with the
//     operating system's own WebDAV client, the default on macOS and
//     Windows, where it needs no kernel driver to be installed.
package mount

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
)

// DefaultReconnect is how long a mount tries to restore a lost session
// before it unmounts itself.
const DefaultReconnect = 2 * time.Minute

// Options configure New.
type Options struct {
	ReadOnly   bool
	AllowOther bool // let other local users access the mount
	Debug      bool // log every FUSE request
	FsName     string
	// Redial re-establishes the session after it is lost; nil disables
	// reconnecting.
	Redial func(ctx context.Context) (*peer.Conn, error)
	// ReconnectFor bounds reconnect attempts (DefaultReconnect if zero,
	// negative disables reconnecting).
	ReconnectFor time.Duration
	// Logf receives diagnostics such as reconnect attempts.
	Logf func(format string, args ...any)
	// ServeOnly (WebDAV) starts the loopback server without mounting it,
	// for use with any WebDAV client.
	ServeOnly bool
}

func init() {
	cli.Register(&cli.Command{
		Name:    "mount",
		Summary: "mount a remote directory locally (FUSE or the OS's WebDAV client)",
		Remote:  true,
		Usage: `Usage: remohop <ID> mount [--ro] [-f] [--webdav|--fuse] [-o OPT[,OPT...]] :<remote dir> <local dir | drive>

Mount a directory of the remote machine locally, so local editors, IDEs and
tools (git, grep, builds) read and write it directly. By default the command
returns once the directory is mounted and keeps serving it in the background
(see ` + "`remohop ps`" + `); stop it with ` + "`remohop umount <local dir | drive>`" + ` or
` + "`remohop stop <PID>`" + `.

How it is mounted, with nothing to install on macOS and Windows:
  Linux    FUSE (/dev/fuse plus the fusermount3 helper from package fuse3
           for regular users; root mounts directly) on an existing
           directory. --webdav uses davfs2 instead, if installed.
  macOS    the system's WebDAV client (mount_webdav) on an existing
           directory. --fuse uses macFUSE instead, if installed (macFUSE is not
           tested; the WebDAV path is).
  Windows  the built-in WebDAV redirector, on a drive letter: give Z: or *
           for a free one.

WebDAV mounts (macOS, Windows, --webdav) are served by a WebDAV server inside
remohop that listens on 127.0.0.1 only, on a random port, below a random 128-bit
path; it refuses requests from web pages and requests not addressed to
127.0.0.1. There is no password (Windows refuses Basic authentication over
plain HTTP): the URL is the credential. It shows in the mount table (` + "`mount`" + `,
` + "`net use`" + `), so other users of this computer who read it can use the mount
while it exists. Finder's and Explorer's metadata files (._*, .DS_Store,
desktop.ini, Thumbs.db) are kept in memory and never written to the remote
machine. Symlinks appear as the files they point to. Each file is uploaded
whole when it is saved and installed atomically.

Windows notes:
  - The WebClient service must be available. Windows Server lacks it until
    ` + "`Install-WindowsFeature WebDAV-Redirector`" + ` (and a restart); if it is
    disabled: ` + "`sc config WebClient start= demand`" + `, ` + "`net start WebClient`" + `.
  - The redirector will not open files above FileSizeLimitInBytes (50 MB by
    default; HKLM\SYSTEM\CurrentControlSet\Services\WebClient\Parameters);
    remohop prints the limit when it mounts. Use ` + "`remohop cp`" + ` for bigger files.

On every platform: changes made on the remote machine show up within about
a second (longer where the OS client caches). If the session drops,
operations fail while remohop reconnects; if it cannot reconnect (default:
within 2 minutes) it unmounts. With FUSE, files keep their remote
permissions and writes are on the remote machine when close() or fsync()
returns.

Flags:
  --ro              read-only mount (enforced by remohop, not only by the client)
  -f                stay in the foreground (Ctrl-C unmounts)
  --webdav          mount through the loopback WebDAV server (the default
                    on macOS and Windows)
  --fuse            mount with FUSE (the default on Linux)
  -o OPTS           comma-separated options:
                      ro             same as --ro
                      reconnect=DUR  how long to try reconnecting (e.g. 30s,
                                     5m; 0 to unmount at once)
                      nomount        WebDAV: do not mount; print the URL for
                                     any WebDAV client (takes no local dir)
                      allow_other    FUSE: let other local users in (needs
                                     user_allow_other in /etc/fuse.conf)
                      debug          log every request

Examples:
  remohop 123456789 mount :~/src/app ~/mnt/app
  remohop 123456789 mount --ro :/var/log /tmp/remote-logs
  remohop 123456789 mount :projects Z:               (Windows)
  remohop 123456789 mount :projects *                (Windows: a free drive)
  remohop 123456789 mount -f -o nomount :/srv/www    (serve WebDAV only)
  remohop umount ~/mnt/app`,
		Run: runMount,
	})
	cli.Register(&cli.Command{
		Name:    "umount",
		Summary: "unmount a directory mounted with `remohop <ID> mount`",
		Usage: `Usage: remohop umount [-l] <local dir | drive>...

Unmount what ` + "`remohop <ID> mount`" + ` mounted (a directory, or a drive such as Z: on
Windows) and stop its background process. A stale mount whose process died
is unmounted too.

Flags:
  -l    lazy/forced: detach a busy mount now (processes still inside get
        errors)

Examples:
  remohop umount ~/mnt/app
  remohop umount Z:`,
		Run: runUmount,
	})
}

type mountArgs struct {
	remote, local string
	foreground    bool
	webdav, fuse  bool
	o             Options
}

func runMount(ctx context.Context, env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("mount", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	var a mountArgs
	var opts string
	fs.BoolVar(&a.o.ReadOnly, "ro", false, "read-only")
	fs.BoolVar(&a.foreground, "f", false, "foreground")
	fs.BoolVar(&a.webdav, "webdav", false, "mount through the loopback WebDAV server")
	fs.BoolVar(&a.fuse, "fuse", false, "mount with FUSE")
	fs.StringVar(&opts, "o", "", "options")
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return cli.ExitUsage
		}
		if fs.NArg() == 0 {
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	nomount := false
	for _, o := range strings.Split(opts, ",") {
		nomount = nomount || strings.TrimSpace(o) == "nomount"
	}
	if a.webdav && a.fuse {
		env.Errorf("mount: --webdav and --fuse exclude each other")
		return cli.ExitUsage
	}
	want := 2
	if nomount {
		want = 1 // nothing is mounted, so there is no target
	}
	if len(pos) != want || !strings.HasPrefix(pos[0], ":") || (want == 2 && strings.HasPrefix(pos[1], ":")) {
		env.Errorf("mount: usage: remohop <ID> mount [--ro] [-f] [--webdav|--fuse] [-o OPTS] :<remote dir> <local dir | drive>")
		return cli.ExitUsage
	}
	a.remote = pos[0][1:]
	if want == 2 {
		a.local = pos[1]
	}
	if opts != "" {
		for _, o := range strings.Split(opts, ",") {
			k, v, hasV := strings.Cut(strings.TrimSpace(o), "=")
			switch {
			case k == "ro" && !hasV:
				a.o.ReadOnly = true
			case k == "allow_other" && !hasV:
				a.o.AllowOther = true
			case k == "debug" && !hasV:
				a.o.Debug = true
			case k == "nomount" && !hasV:
				a.o.ServeOnly = true
			case k == "reconnect" && hasV:
				d, err := time.ParseDuration(v)
				if err != nil {
					env.Errorf("mount: -o reconnect=%s: %v", v, err)
					return cli.ExitUsage
				}
				a.o.ReconnectFor = d
				if d == 0 {
					a.o.ReconnectFor = -1
				}
			default:
				env.Errorf("mount: unknown option %q", o)
				return cli.ExitUsage
			}
		}
	}
	return mountCmd(ctx, env, a)
}

func runUmount(ctx context.Context, env *cli.Env, args []string) int {
	fs := flag.NewFlagSet("umount", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	lazy := fs.Bool("l", false, "lazy unmount")
	if err := fs.Parse(args); err != nil {
		return cli.ExitUsage
	}
	if fs.NArg() == 0 {
		env.Errorf("umount: need a directory")
		return cli.ExitUsage
	}
	code := cli.ExitOK
	for _, d := range fs.Args() {
		if err := umountCmd(d, *lazy); err != nil {
			env.Errorf("umount %s: %v", d, err)
			code = cli.ExitError
			continue
		}
		fmt.Fprintf(env.Stdout, "unmounted %s\n", d)
	}
	return code
}
