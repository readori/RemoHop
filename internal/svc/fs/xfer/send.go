package xfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/svc/fs/rfs"
)

func init() {
	cli.Register(&cli.Command{
		Name:    "send",
		Summary: "send files to the remote user's Downloads folder",
		Remote:  true,
		Usage: `Usage: remohop <ID> send [-r] [-q] [--json] <file>...

Send local files to the remote user's download directory, without knowing
any remote path: the remote side resolves it (XDG_DOWNLOAD_DIR from
~/.config/user-dirs.dirs on Linux, the Downloads known folder on Windows,
else ~/Downloads; REMOHOP_DOWNLOADS_DIR in the host's environment overrides it)
and creates it if needed.

Nothing is ever overwritten: if the name is taken, the file is saved as
"name (1).ext", "name (2).ext", ...; the remote side picks the name
atomically, so concurrent sends never collide. Each file appears complete
or not at all. Directories need -r and get a fresh name the same way.
Permission bits are kept minus the remote umask; modification times are
kept. The final remote paths are printed (names with control characters
replaced; --json carries them exactly).

Flags:
  -r       send directories recursively
  -q       no progress display
  --json   JSON array of {source, path, bytes, dir}

Examples:
  remohop 123456789 send report.pdf
  remohop 123456789 send -r ./photos notes.txt`,
		Run: runSend,
	})
}

// SendOptions control Send.
type SendOptions struct {
	Recursive bool
	Parallel  int
	Progress  *Progress
}

// Sent is one item delivered by Send.
type Sent struct {
	Source string `json:"source"`
	Path   string `json:"path"` // final remote path
	Bytes  int64  `json:"bytes"`
	Dir    bool   `json:"dir,omitempty"`
}

// Send delivers local files and (with Recursive) directories into the
// host user's download directory under names that do not exist yet. It
// returns what was delivered; failures are reported per item in err
// (wrapping ErrPartial) while the rest still go through.
func Send(ctx context.Context, c *rfs.Client, srcs []string, o SendOptions) ([]Sent, error) {
	if o.Parallel <= 0 {
		o.Parallel = DefaultParallel
	}
	if o.Progress == nil {
		o.Progress = &Progress{}
	}
	dir, err := c.DownloadsDir(ctx)
	if err != nil {
		return nil, err
	}
	sep := "/"
	if info, err := c.Info(ctx); err == nil && info.OS == "windows" {
		sep = `\` // report paths the way the host writes them
	}
	mask := destUmask(ctx, c, true)
	sent := []Sent{}
	var errs []error
	for _, s := range srcs {
		item, err := sendOne(ctx, c, dir, sep, s, mask, o)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		sent = append(sent, *item)
	}
	if len(errs) > 0 {
		return sent, fmt.Errorf("%w: %w", ErrPartial, errors.Join(errs...))
	}
	return sent, nil
}

func sendOne(ctx context.Context, c *rfs.Client, dir, sep, src string, mask fs.FileMode, o SendOptions) (*Sent, error) {
	join := func(name string) string { return strings.TrimRight(dir, `/\`) + sep + name }
	abs, err := filepath.Abs(src)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}
	base := filepath.Base(abs)
	rdir := remoteFS{c}
	e := localEntry(fi, "")
	switch {
	case fi.Mode().IsRegular():
		perm := e.mode & permBits &^ mask
		start := o.Progress.BytesDone.Load()
		info, err := sendFile(ctx, c, abs, join(base), e, perm, o.Progress)
		if err != nil {
			return nil, err
		}
		return &Sent{Source: src, Path: join(info.Name()), Bytes: o.Progress.BytesDone.Load() - start}, nil
	case fi.IsDir():
		if !o.Recursive {
			return nil, fmt.Errorf("%s is a directory (use -r)", src)
		}
		// mkdir fails if the name exists, so trying names in turn picks
		// a fresh one atomically.
		var target string
		for i := 0; ; i++ {
			name := base
			if i > 0 {
				name = fmt.Sprintf("%s (%d)", base, i)
			}
			target = join(name)
			err := c.Mkdir(ctx, target, fi.Mode().Perm()&^mask|0o700)
			if err == nil {
				break
			}
			if !errors.Is(err, fs.ErrExist) || i > 10000 {
				return nil, err
			}
		}
		cp := &copier{ctx: ctx, c: c, src: localFS{}, dst: rdir, mask: mask, prog: o.Progress,
			o: CopyOptions{Recursive: true, PreserveTimes: true, Parallel: o.Parallel, NoResume: true}}
		start := o.Progress.BytesDone.Load()
		cp.dirFixes = append(cp.dirFixes, dirFix{path: target, e: e})
		cp.run(cp.planTree(abs, target, e))
		if len(cp.res.Errors) > 0 {
			return nil, fmt.Errorf("%s: %d errors, first: %s", src, len(cp.res.Errors), cp.res.Errors[0])
		}
		return &Sent{Source: src, Path: target, Bytes: o.Progress.BytesDone.Load() - start, Dir: true}, nil
	}
	return nil, fmt.Errorf("%s: not a regular file or directory", src)
}

func sendFile(ctx context.Context, c *rfs.Client, sp, dp string, e *entry, perm fs.FileMode, prog *Progress) (*rfs.FileInfo, error) {
	f, err := os.Open(sp)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opts := rfs.PutOptions{Mode: perm, ModTime: e.mtime, Unique: true}
	b, err := io.ReadAll(io.LimitReader(f, rfs.SmallFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) <= rfs.SmallFile {
		info, err := c.Put(ctx, dp, b, opts)
		if err == nil {
			prog.addBytes(int64(len(b)))
		}
		return info, err
	}
	return c.Upload(ctx, dp, countingReader{io.MultiReader(bytes.NewReader(b), f), prog}, opts)
}

func runSend(ctx context.Context, env *cli.Env, args []string) int {
	fset := cli.NewFlagSet(env, "send", "")
	var o SendOptions
	var asJSON, quiet bool
	fset.BoolVar(&o.Recursive, "r", false, "recursive")
	fset.BoolVar(&o.Recursive, "R", false, "recursive")
	fset.BoolVar(&quiet, "q", false, "no progress")
	fset.BoolVar(&asJSON, "json", false, "JSON output")
	pos, err := parseArgs(fset, args)
	if err != nil {
		return cli.FlagExit(err)
	}
	if len(pos) == 0 {
		env.Errorf("send: name at least one file (see `remohop help send`)")
		return cli.ExitUsage
	}
	for _, p := range pos {
		if len(p) > 0 && p[0] == ':' {
			env.Errorf("send: %s: send takes local files; use cp for remote paths", p)
			return cli.ExitUsage
		}
	}
	c, code := connect(ctx, env)
	if c == nil {
		return code
	}
	defer c.Close()
	o.Progress = &Progress{}
	stop := func() {}
	if !quiet && !asJSON && isTerminal(env.Stderr) {
		stop = showProgress(env.Stderr, o.Progress)
	}
	sent, err := Send(ctx, c, pos, o)
	stop()
	if asJSON {
		printJSON(env.Stdout, sent)
	} else {
		for _, s := range sent {
			fmt.Fprintf(env.Stdout, "%s -> %s\n", cli.Sanitize(s.Source), cli.Sanitize(s.Path))
		}
	}
	if err != nil {
		env.Errorf("send: %v", err)
		return cli.ExitError
	}
	return cli.ExitOK
}
