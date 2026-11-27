package grant

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
)

// The host's config directory holds what decides who may do what: the
// grants file (re-read whenever it changes), the pairing tokens, the
// identity key, the settings and the status file with the temporary
// password. A granted session that could read or change it could make
// itself a full-access client. So no grant, whatever its file roots, gives
// access to it: every path a granted file operation acts on is checked
// against the protected directories, after its links are resolved.
//
// A session that may write is also refused changes to the directories
// above a protected one (renaming, removing or replacing them, or changing
// their attributes), so that it cannot move the config directory out of
// the way, edit it under another name and move it back, and it may not
// create or move a symbolic link that leads into a protected directory or
// to a directory above one.
var protected = struct {
	sync.Mutex
	dirs map[string]int
}{dirs: map[string]int{}}

// Protect adds dir to the directories no grant gives access to, and
// returns a function that removes it again. The host daemon protects its
// config directory for as long as it serves. Protecting the same directory
// more than once is counted.
func Protect(dir string) (release func()) {
	if dir == "" {
		return func() {}
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	dir = filepath.Clean(dir)
	protected.Lock()
	protected.dirs[dir]++
	protected.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			protected.Lock()
			defer protected.Unlock()
			if protected.dirs[dir]--; protected.dirs[dir] <= 0 {
				delete(protected.dirs, dir)
			}
		})
	}
}

// ProtectedDirs returns the protected directories, each as given and, if
// different, with its links resolved.
func ProtectedDirs() []string {
	protected.Lock()
	dirs := make([]string, 0, len(protected.dirs))
	for d := range protected.dirs {
		dirs = append(dirs, d)
	}
	protected.Unlock()
	slices.Sort(dirs)
	out := slices.Clone(dirs)
	for _, d := range dirs {
		if r, err := RealPath(d); err == nil && !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// ProtectedError refuses access to a protected directory.
type ProtectedError struct {
	Path, Dir string
	// Above is set when the path is a directory above Dir (or a link
	// leading to one) and the refused access would change it.
	Above bool
}

func (e *ProtectedError) Error() string {
	if e.Above {
		return fmt.Sprintf("%s holds RemoHop's configuration directory %s, which no grant may change, move or replace", e.Path, e.Dir)
	}
	return fmt.Sprintf("%s is in RemoHop's configuration directory %s, which no grant gives access to", e.Path, e.Dir)
}

// foldCase says whether paths are compared without regard to case: the
// default file systems of Windows and macOS ignore it.
var foldCase = runtime.GOOS == "windows" || runtime.GOOS == "darwin"

// withinFold is within, ignoring case where the file system does.
func withinFold(p, root string) bool {
	if foldCase {
		p, root = strings.ToLower(p), strings.ToLower(root)
	}
	return within(p, root)
}

// checkProtected refuses access a to the places in paths (clean absolute
// host paths, as given and as resolved) when one of them lies in a
// protected directory or, for a change, is one of the directories above
// it.
func checkProtected(a Access, paths ...string) error {
	dirs := ProtectedDirs()
	if len(dirs) == 0 {
		return nil
	}
	for _, p := range paths {
		if p == "" {
			continue
		}
		for _, d := range dirs {
			if withinFold(p, d) {
				return &ProtectedError{Path: p, Dir: d}
			}
			if a == Write && withinFold(d, p) {
				return &ProtectedError{Path: p, Dir: d, Above: true}
			}
		}
	}
	return nil
}

// protectedPlaces returns what checkProtected looks at for host path p:
// p itself, p with every link resolved, and p with the links of its parent
// resolved (the entry itself, for operations that do not follow it). ".."
// components are resolved as the OS does, after the links before them.
func protectedPlaces(p string) ([]string, error) {
	if !filepath.IsAbs(p) {
		return nil, fmt.Errorf("%s is not an absolute path", p)
	}
	followed, err := realPath(p)
	if err != nil {
		return nil, err
	}
	out := []string{filepath.Clean(p), followed}
	// The parent as the OS finds it: everything before the last
	// component, not cleaned, so that ".." after a link means what it
	// means to the OS.
	trimmed := strings.TrimRightFunc(p, func(c rune) bool { return c < 0x80 && os.IsPathSeparator(uint8(c)) })
	i := strings.LastIndexFunc(trimmed, func(c rune) bool { return c < 0x80 && os.IsPathSeparator(uint8(c)) })
	if i < len(filepath.VolumeName(trimmed)) {
		return out, nil // a volume root
	}
	base := trimmed[i+1:]
	if base == "" || base == "." || base == ".." {
		return out, nil
	}
	dir := trimmed[:i+1]
	parent, err := realPath(dir)
	if err != nil {
		return nil, err
	}
	return append(out, filepath.Join(parent, base)), nil
}

// CheckProtected refuses access a to host path p when it reaches into a
// protected directory (see Protect), or for a change, when it is a
// directory above one.
func CheckProtected(p string, a Access) error {
	if len(ProtectedDirs()) == 0 {
		return nil
	}
	places, err := protectedPlaces(p)
	if err != nil {
		return err
	}
	return checkProtected(a, places...)
}

// CheckLinkTarget decides whether a symbolic link with target text target
// may be at host path link (made there, renamed there or linked there)
// under a grant: it may not lead into a protected directory or to a
// directory above one, whatever the grant's roots.
func CheckLinkTarget(link, target string) error {
	if len(ProtectedDirs()) == 0 {
		return nil
	}
	if target == "" {
		return errors.New("empty link target")
	}
	t := target
	if !filepath.IsAbs(t) {
		if filepath.VolumeName(t) != "" || os.IsPathSeparator(t[0]) {
			return fmt.Errorf("link target %q is relative to a drive or volume, which is not allowed under this grant", target)
		}
		parent, err := realPath(filepath.Dir(filepath.Clean(link)))
		if err != nil {
			return err
		}
		t = parent + string(filepath.Separator) + t
	}
	rt, err := realPath(t)
	if err != nil {
		return err
	}
	for _, d := range ProtectedDirs() {
		if withinFold(rt, d) || withinFold(d, rt) {
			return fmt.Errorf("a symbolic link made under a grant may not lead to RemoHop's configuration directory %s or a directory above it; %q leads to %s", d, target, rt)
		}
	}
	return nil
}

// RootsContaining returns the file roots of r that contain one of dirs,
// so that whoever issues a grant can be told that the config directory in
// them stays out of reach. ReadAll and WriteAll are the caller's to check.
func (r *Rules) RootsContaining(dirs []string) []string {
	var out []string
	for _, root := range append(slices.Clone(r.ReadRoots), r.WriteRoots...) {
		rr, err := RealPath(root)
		if err != nil {
			rr = root
		}
		for _, d := range dirs {
			rd, err := RealPath(d)
			if err != nil {
				rd = d
			}
			if (withinFold(d, root) || withinFold(rd, rr)) && !slices.Contains(out, root) {
				out = append(out, root)
			}
		}
	}
	return out
}
