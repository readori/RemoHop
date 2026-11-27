package xfer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"golang.org/x/term"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

func init() {
	cli.Register(&cli.Command{
		Name:    "cp",
		Summary: "copy files to or from the remote machine",
		Remote:  true,
		Usage: `Usage: remohop <ID> cp [-r] [-p] [-j N] [--no-resume] [--json] <src>... <dst>

Copy files between this machine and the remote one. Remote paths start with
':' (":/var/log/syslog", ":notes.txt" is relative to the remote home, ":~/x"
works too); local paths do not. Either every source is local and dst remote
(upload) or every source is remote and dst local (download).

Semantics follow cp/scp:
  - if dst is an existing directory, each source is copied into it under its
    own name (several sources require this);
  - otherwise the single source is copied to dst (with -r, a directory
    source becomes dst);
  - symlinks named on the command line are followed; symlinks inside copied
    directories are recreated as symlinks with the same target;
  - permission bits are copied minus the umask of the receiving side (like
    cp); -p preserves them exactly and also preserves modification times;
    files downloaded from the remote machine never get setuid/setgid bits;
  - every file is written to a temporary name and renamed into place, so an
    interrupted copy never leaves a truncated file under the final name;
  - files of 8 MiB or more are resumable: an interrupted copy keeps its data
    in a hidden ".<name>.remohop-partial-<id>" file next to the destination, and
    running the same copy again continues after it, if the source is
    unchanged (same size and mtime) and the kept data verifies against the
    source (SHA-256 on both sides). The next successful copy to that
    destination removes such leftovers. --no-resume starts over.

A download only ever writes inside the destination: names from the remote
machine are validated and existing symlinks are never followed below the
destination.

"-" as the only source uploads stdin to a remote file; "-" as dst writes the
remote files to stdout.

Flags:
  -r, -R     copy directories recursively
  -p         preserve modification times and exact permission bits
  -j N       transfer N large files in parallel (default 8); small files
             and directories go at least 48 at a time
  -q         no progress display and no summary
  --no-resume  do not resume interrupted copies of large files
  --json     print a JSON summary on stdout

Progress is shown on stderr when it is a terminal, and a summary line
("copied 12 files and 3 directories (4.2 MB) in 0.8s") on stderr when the
copy succeeded.

Examples:
  remohop 123456789 cp ./build.tar.gz :/tmp/
  remohop 123456789 cp -r ./site :/var/www/site
  remohop 123456789 cp :/var/log/syslog :/var/log/auth.log ./logs/
  remohop 123456789 cp -rp :project .
  echo hello | remohop 123456789 cp - :/tmp/hello.txt
  remohop 123456789 cp :/etc/os-release -`,
		Run: runCp,
	})
	cli.Register(&cli.Command{
		Name:    "sync",
		Summary: "one-way directory sync with the remote machine",
		Remote:  true,
		Usage: `Usage: remohop <ID> sync [--delete] [-n] [--exclude PATTERN]... [--checksum] [--perms] [--no-resume] [-v] [--json] <src> <dst>

Make the directory dst a mirror of the directory src. Exactly one of them is
remote (prefixed with ':'). Both paths name the directories themselves:
"sync :/srv/app ./app" makes ./app hold exactly what /srv/app holds, creating
./app if needed. Trailing slashes make no difference (unlike rsync).

A file is transferred when it is missing in dst, differs in type or size, or
its modification time differs (whole seconds); with --checksum, files of
equal size are compared by SHA-256 instead (each side hashes its own files;
the remote hash is computed on the remote machine). Unchanged files are not
touched. Modification times and permission bits are preserved, except that
group/other write permission is only granted where the receiving side's
umask allows it (--perms copies modes exactly); downloads never get
setuid/setgid bits. Symlinks are recreated, never followed (nothing is ever
written, chmod'ed or touched through a link); special files are skipped.

Flags:
  --delete          delete files in dst that are not in src (never outside
                    dst, never excluded files; skipped entirely if part of
                    src could not be read)
  -n, --dry-run     show what would change, change nothing
  --exclude PAT     skip matching paths, gitignore-style (repeatable):
                      *.log         that name at any depth
                      node_modules  a file or directory at any depth
                      build/        directories only
                      /dist, a/b    anchored at the sync root
                      **/tmp, a/**  "**" crosses directories
                    excluded directories are skipped entirely
  --checksum        compare content hashes instead of modification times
  --perms           copy permission bits exactly, ignoring the umask
  --no-resume       do not resume interrupted transfers of large files
                    (files of 8 MiB or more resume as with cp)
  -j N              transfer N large files in parallel (default 8); small
                    files and directories go at least 48 at a time
  -v                list every change (a dry run always does)
  -q                no progress display
  --json            print a JSON report on stdout

Output lines: "+ path" transferred, "d path/" directory created,
"~ path" mode/time updated, "- path" deleted; then a summary.

Examples:
  remohop 123456789 sync :/srv/app ./app
  remohop 123456789 sync --delete --exclude node_modules --exclude '*.log' ./site :/var/www/site
  remohop 123456789 sync -n --delete ./src :src`,
		Run: runSync,
	})
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(s string) error { *m = append(*m, s); return nil }

// parseArgs parses flags anywhere among the arguments (people write
// "cp a b -r"), stopping at "--". Clusters of single-letter boolean flags
// ("-rp") are expanded.
func parseArgs(fs *flag.FlagSet, args []string) ([]string, error) {
	var expanded []string
	for i, a := range args {
		if a == "--" {
			expanded = append(expanded, args[i:]...)
			break
		}
		expanded = append(expanded, expandCluster(fs, a)...)
	}
	args = expanded
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		if args[0] == "--" {
			return append(pos, args[1:]...), nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func expandCluster(fs *flag.FlagSet, a string) []string {
	if len(a) < 3 || a[0] != '-' || a[1] == '-' || strings.Contains(a, "=") {
		return []string{a}
	}
	var out []string
	for _, r := range a[1:] {
		f := fs.Lookup(string(r))
		if f == nil {
			return []string{a}
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !bf.IsBoolFlag() {
			return []string{a}
		}
		out = append(out, "-"+string(r))
	}
	return out
}

// isTerminal reports whether w is a terminal.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// showProgress redraws a one-line progress display on w until stop is
// called.
func showProgress(w io.Writer, p *Progress) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		start := time.Now()
		t := time.NewTicker(250 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				fmt.Fprint(w, "\r\x1b[K")
				return
			case <-t.C:
				b := p.BytesDone.Load()
				rate := float64(b) / time.Since(start).Seconds()
				fmt.Fprintf(w, "\r\x1b[K%d/%d files  %s/%s  %s/s", p.FilesDone.Load(), p.FilesTotal.Load(),
					humanBytes(b), humanBytes(p.BytesTotal.Load()), humanBytes(int64(rate)))
			}
		}
	}()
	return func() { close(done); wg.Wait() }
}

func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTPE"[exp])
}

// printJSON writes v as indented JSON. encoding/json escapes ASCII control
// characters but passes DEL, C1 controls and other characters a terminal
// may act on through raw; those (as judged by cli.Sanitize) are written as
// \u escapes, so host-chosen names cannot drive the terminal of someone
// looking at the output. They can only occur inside strings, where the
// escape keeps the value intact.
func printJSON(w io.Writer, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.Encode(v)
	var out strings.Builder
	for _, r := range buf.String() {
		if r >= 0x7f && cli.Sanitize(string(r)) != string(r) {
			if r > 0xffff {
				r1, r2 := utf16.EncodeRune(r)
				fmt.Fprintf(&out, "\\u%04x\\u%04x", r1, r2)
			} else {
				fmt.Fprintf(&out, "\\u%04x", r)
			}
			continue
		}
		out.WriteRune(r)
	}
	io.WriteString(w, out.String())
}

func connect(ctx context.Context, env *cli.Env) (*rfs.Client, int) {
	conn, err := env.Conn(ctx)
	if err != nil {
		env.Errorf("%v", err)
		return nil, cli.ExitConnect
	}
	c, err := rfs.NewClient(ctx, conn)
	if err != nil {
		env.Errorf("%v", err)
		return nil, cli.ExitConnect
	}
	return c, cli.ExitOK
}

func runCp(ctx context.Context, env *cli.Env, args []string) int {
	fs := cli.NewFlagSet(env, "cp", "")
	var o CopyOptions
	var asJSON, quiet bool
	fs.BoolVar(&o.Recursive, "r", false, "recursive")
	fs.BoolVar(&o.Recursive, "R", false, "recursive")
	fs.BoolVar(&o.PreserveTimes, "p", false, "preserve modification times")
	fs.BoolVar(&o.NoResume, "no-resume", false, "do not resume")
	fs.IntVar(&o.Parallel, "j", DefaultParallel, "parallel transfers")
	fs.BoolVar(&quiet, "q", false, "no progress")
	fs.BoolVar(&asJSON, "json", false, "JSON summary")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return cli.FlagExit(err)
	}
	if len(pos) < 2 {
		env.Errorf("cp: need a source and a destination (see `remohop help cp`)")
		return cli.ExitUsage
	}
	var srcs []Path
	for _, a := range pos[:len(pos)-1] {
		srcs = append(srcs, ParsePath(a))
	}
	dst := ParsePath(pos[len(pos)-1])
	for _, s := range srcs {
		if s.Remote == dst.Remote {
			env.Errorf("cp: %s -> %s: exactly one side must be remote (prefix remote paths with ':')", s, dst)
			return cli.ExitUsage
		}
	}
	if len(srcs) > 1 && !srcs[0].Remote {
		for _, s := range srcs {
			if s.Path == "-" {
				env.Errorf("cp: \"-\" must be the only source")
				return cli.ExitUsage
			}
		}
	}

	c, code := connect(ctx, env)
	if c == nil {
		return code
	}
	defer c.Close()
	o.Stdin, o.Stdout = env.Stdin, env.Stdout
	o.Progress = &Progress{}
	if !quiet && !asJSON && dst.Path != "-" && isTerminal(env.Stderr) {
		stop := showProgress(env.Stderr, o.Progress)
		defer stop()
	}
	res, err := Copy(ctx, c, srcs, dst, o)
	if res == nil {
		env.Errorf("cp: %v", err)
		return cli.ExitError
	}
	if asJSON {
		if res.Errors == nil {
			res.Errors = []string{}
		}
		printJSON(env.Stdout, res)
	}
	for _, e := range res.Errors {
		env.Errorf("cp: %s", e)
	}
	if err != nil {
		return cli.ExitError
	}
	if !quiet && !asJSON && dst.Path != "-" {
		// On stderr, beside the progress line it replaces: stdout stays
		// what it always was (empty, or the data of a "-" destination).
		fmt.Fprintln(env.Stderr, copySummary(res))
	}
	return cli.ExitOK
}

// copySummary is the line cp prints when it is done, such as "copied 12
// files and 3 directories (4.2 MB) in 0.8s".
func copySummary(r *CopyResult) string {
	count := func(n int, one, many string) string {
		if n == 1 {
			return "1 " + one
		}
		return fmt.Sprintf("%d %s", n, many)
	}
	what := []string{count(r.Files, "file", "files")}
	if r.Dirs > 0 {
		what = append(what, count(r.Dirs, "directory", "directories"))
	}
	if r.Symlinks > 0 {
		what = append(what, count(r.Symlinks, "symlink", "symlinks"))
	}
	list := what[0]
	if len(what) > 1 {
		list = strings.Join(what[:len(what)-1], ", ") + " and " + what[len(what)-1]
	}
	size := humanBytes(r.Bytes)
	if r.Resumed > 0 {
		size += ", and " + humanBytes(r.Resumed) + " kept from an interrupted copy"
	}
	return fmt.Sprintf("copied %s (%s) in %.1fs", list, size, r.Seconds)
}

func runSync(ctx context.Context, env *cli.Env, args []string) int {
	fs := cli.NewFlagSet(env, "sync", "")
	var o SyncOptions
	var excludes multiFlag
	var asJSON, verbose, quiet bool
	fs.BoolVar(&o.Delete, "delete", false, "delete extraneous files")
	fs.BoolVar(&o.DryRun, "n", false, "dry run")
	fs.BoolVar(&o.DryRun, "dry-run", false, "dry run")
	fs.Var(&excludes, "exclude", "exclude pattern (repeatable)")
	fs.BoolVar(&o.Checksum, "checksum", false, "compare content hashes")
	fs.BoolVar(&o.ExactPerms, "perms", false, "copy permission bits exactly")
	fs.BoolVar(&o.NoResume, "no-resume", false, "do not resume")
	fs.IntVar(&o.Parallel, "j", DefaultParallel, "parallel transfers")
	fs.BoolVar(&verbose, "v", false, "list changes")
	fs.BoolVar(&quiet, "q", false, "no progress")
	fs.BoolVar(&asJSON, "json", false, "JSON report")
	pos, err := parseArgs(fs, args)
	if err != nil {
		return cli.FlagExit(err)
	}
	if len(pos) != 2 {
		env.Errorf("sync: need exactly a source and a destination (see `remohop help sync`)")
		return cli.ExitUsage
	}
	src, dst := ParsePath(pos[0]), ParsePath(pos[1])
	if src.Remote == dst.Remote {
		env.Errorf("sync: exactly one of source and destination must be remote (prefix it with ':')")
		return cli.ExitUsage
	}
	o.Excludes = excludes
	if _, err := NewExcluder(o.Excludes); err != nil {
		env.Errorf("sync: %v", err)
		return cli.ExitUsage
	}

	c, code := connect(ctx, env)
	if c == nil {
		return code
	}
	defer c.Close()
	o.Progress = &Progress{}
	stop := func() {}
	if !quiet && !asJSON && !o.DryRun && isTerminal(env.Stderr) {
		stop = showProgress(env.Stderr, o.Progress)
	}
	res, err := Sync(ctx, c, src, dst, o)
	stop()
	if res == nil {
		env.Errorf("sync: %v", err)
		return cli.ExitError
	}
	if asJSON {
		if res.Errors == nil {
			res.Errors = []string{}
		}
		printJSON(env.Stdout, res)
	} else {
		if verbose || o.DryRun {
			for _, d := range res.CreatedDirs {
				fmt.Fprintf(env.Stdout, "d %s/\n", cli.Sanitize(d))
			}
			for _, f := range res.Transferred {
				fmt.Fprintf(env.Stdout, "+ %s\n", cli.Sanitize(f))
			}
			for _, f := range res.Updated {
				fmt.Fprintf(env.Stdout, "~ %s\n", cli.Sanitize(f))
			}
			for _, f := range res.Deleted {
				fmt.Fprintf(env.Stdout, "- %s\n", cli.Sanitize(f))
			}
		}
		prefix := ""
		if o.DryRun {
			prefix = "dry run: would have "
		}
		fmt.Fprintf(env.Stdout, "%stransferred %d (%s), deleted %d, updated %d, created %d dirs; %d unchanged, %d skipped; %.2fs\n",
			prefix, len(res.Transferred), humanBytes(res.Bytes), len(res.Deleted), len(res.Updated),
			len(res.CreatedDirs), res.Unchanged, len(res.Skipped), res.Seconds)
	}
	for _, s := range res.Skipped {
		env.Errorf("sync: skipped special file %s", s)
	}
	for _, e := range res.Errors {
		env.Errorf("sync: %s", e)
	}
	if err != nil && !errors.Is(err, ErrPartial) {
		env.Errorf("sync: %v", err)
	}
	if err != nil {
		return cli.ExitError
	}
	return cli.ExitOK
}
