package mcp

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/readori/RemoHop/internal/config"
)

// Every local file the MCP server reads or writes on a model's behalf goes
// through guardLocal, and every write through writeLocal. Content written
// here often comes from a remote machine, and the model driving the tools
// may itself have been misled, so these paths must not be usable to replace
// shell startup files or to read or tamper with RemoHop's own credentials.

// localPath expands ~ and insists on an absolute path: the server's working
// directory is whatever the agent host happened to launch it from, so a
// relative path would land somewhere the model cannot predict.
func localPath(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("local path %q must be absolute", p)
	}
	return filepath.Clean(p), nil
}

// resolve evaluates symlinks in p. If p itself does not exist, its parent is
// resolved and the name re-attached, so a path that is about to be created is
// judged by where it would really land.
func resolve(p string) (string, error) {
	r, err := filepath.EvalSymlinks(p)
	if err == nil {
		return r, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(p))
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, filepath.Base(p)), nil
}

// guardLocal validates a local path argument and refuses anything that
// resolves into RemoHop's configuration directory (identity key, pairing
// tokens, pinned host keys). It returns the cleaned absolute path.
func guardLocal(arg string) (string, error) {
	p, err := localPath(arg)
	if err != nil {
		return "", err
	}
	cfg, err := config.Dir()
	if err != nil {
		return "", fmt.Errorf("locating RemoHop config directory: %w", err)
	}
	cfgReal, err := filepath.EvalSymlinks(cfg)
	if err != nil {
		return "", err
	}
	real, err := resolve(p)
	if err != nil {
		// A missing parent is reported by the later open/create with a
		// better message; only paths that exist can point into the config.
		if errors.Is(err, fs.ErrNotExist) {
			return p, nil
		}
		return "", err
	}
	if real == cfgReal || strings.HasPrefix(real, cfgReal+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is inside RemoHop's configuration directory, which holds credentials; the MCP tools never read or write there", p)
	}
	return p, nil
}

// writeLocal streams r into dest and returns the byte count. The data goes to
// a private temporary file in dest's directory first, so a failed transfer
// never leaves a partial file. Without overwrite, an existing dest (including
// a dangling or live symlink) is refused, and the final step is a hard link,
// which fails atomically if dest appears meanwhile. With overwrite, rename
// replaces dest itself: a symlink at dest is replaced, never followed.
// More than max bytes is an error.
func writeLocal(dest string, r io.Reader, perm fs.FileMode, overwrite bool, max int64) (int64, error) {
	if _, err := os.Lstat(dest); err == nil && !overwrite {
		return 0, fmt.Errorf("%s already exists; pass overwrite=true to replace it", dest)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".remohop-write-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())
	n, err := io.Copy(tmp, io.LimitReader(r, max+1))
	if err == nil && n > max {
		err = fmt.Errorf("data exceeds the %d byte limit", max)
	}
	if err == nil {
		err = tmp.Chmod(perm)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return 0, err
	}
	if overwrite {
		err = os.Rename(tmp.Name(), dest)
	} else if err = os.Link(tmp.Name(), dest); errors.Is(err, fs.ErrExist) {
		err = fmt.Errorf("%s already exists; pass overwrite=true to replace it", dest)
	}
	if err != nil {
		return 0, err
	}
	return n, nil
}
