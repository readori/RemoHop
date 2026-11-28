package ffmpeg

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/readori/RemoHop/internal/config"
)

// Another RemoHop instance's ffmpeg: the Windows service's.
//
// The Windows service (unattended access) runs as LocalSystem, with its own
// RemoHop directory (%ProgramData%\RemoHop) and its own environment. It
// finds ffmpeg as Find does, with that environment and that directory. It
// does not look at a copy in a user's %LocalAppData%, and must not: that
// user could replace it with any program, which the service would then run
// as LocalSystem. So the service gets a copy of its own, the same pinned
// build checked the same way (Install), in its own directory, which only
// SYSTEM and the Administrators can change. Installing it needs an
// administrator: `remohop ffmpeg install --service` from an elevated
// prompt, or the desktop app's administrator step for unattended access.

// Target is a RemoHop instance other than this process that runs ffmpeg.
type Target struct {
	// Name says which, for people ("the RemoHop service").
	Name string
	// ConfigDir is its RemoHop directory. Its copy of ffmpeg is in the
	// "ffmpeg" directory there (CopyDir), as Dir is for that instance.
	ConfigDir string
	// Getenv reads its environment: PathEnv, PATH and DownloadEnv. Nil
	// means an empty one.
	Getenv func(string) string
	// Secure, if set, is called with CopyDir before anything in it is
	// looked at, and again after this process installed into it
	// (installed). It creates the directory if need be, moves aside one
	// that cannot be trusted, and leaves it and everything in it
	// changeable only by the accounts the instance trusts. note says what
	// it moved aside, if anything.
	Secure func(dir string, installed bool) (note string, err error)
}

// Service, when set, finds the RemoHop service that shares this computer's
// screen with a directory of its own: on Windows, the service installed
// with --unattended (internal/service sets it). ok is false when there is
// none.
var Service func(ctx context.Context) (t Target, ok bool, err error)

// CopyDir is where t's copy of ffmpeg is.
func (t Target) CopyDir() string { return filepath.Join(t.ConfigDir, "ffmpeg") }

func (t Target) getenv(k string) string {
	if t.Getenv == nil {
		return ""
	}
	return t.Getenv(k)
}

// FindFor is Find as t sees it: its REMOHOP_FFMPEG, else ffmpeg on its
// PATH, else its copy. An error other than ErrNotFound means its copy
// could not be looked at (its directory is the administrators' only).
func FindFor(t Target) (Found, error) {
	if p := t.getenv(PathEnv); p != "" {
		return Found{Path: p, Kind: KindEnv}, nil
	}
	if p, ok := lookPathIn(t.getenv("PATH")); ok {
		return Found{Path: p, Kind: KindSystem}, nil
	}
	if _, ok := Pinned(); ok {
		p := filepath.Join(t.CopyDir(), exeName())
		fi, err := os.Stat(p)
		switch {
		case err == nil && fi.Mode().IsRegular():
			return Found{Path: p, Kind: KindRemoHop}, nil
		case err != nil && !errors.Is(err, fs.ErrNotExist):
			return Found{}, err
		}
	}
	return Found{}, ErrNotFound
}

// lookPathIn finds ffmpeg in the directories of a PATH value. Relative
// entries are skipped: they would depend on the instance's working
// directory.
func lookPathIn(path string) (string, bool) {
	for _, d := range filepath.SplitList(path) {
		if d == "" || !filepath.IsAbs(d) {
			continue
		}
		p := filepath.Join(d, exeName())
		fi, err := os.Stat(p)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		if runtime.GOOS != "windows" && fi.Mode().Perm()&0o111 == 0 {
			continue
		}
		return p, true
	}
	return "", false
}

// DownloadDisabledFor reports whether downloads for t are switched off, and
// by what. Any switch counts: this process's (DownloadDisabled: its
// environment, this user's setting) and t's (its environment and its own
// setting, which for the Windows service are the machine's).
func DownloadDisabledFor(t Target) (bool, string) {
	if off, by := DownloadDisabled(); off {
		return off, by
	}
	if v := strings.TrimSpace(t.getenv(DownloadEnv)); v != "" && envOff(v) {
		return true, DownloadEnv + "=" + v + " in the environment of " + t.Name
	}
	if t.ConfigDir != "" {
		if m, err := config.AllIn(t.ConfigDir); err == nil {
			if v := m[SettingDownload]; v != "" && !config.Bool(v, true) {
				return true, "the setting " + SettingDownload + " (" + v + ") of " + t.Name
			}
		}
	}
	return false, ""
}

// Plan is what RemoHop would do about another instance's ffmpeg.
type Plan struct {
	// Found is the ffmpeg it uses, if any.
	Found *Found `json:"found,omitempty"`
	// Install: it has none, and RemoHop can install its copy for it.
	Install bool `json:"install,omitempty"`
	// Why it has none and RemoHop cannot install one (no build for this
	// system, downloads switched off).
	Why string `json:"why,omitempty"`
}

// PlanFor decides whether to install RemoHop's copy for t: only when t
// finds no ffmpeg at all, RemoHop has a build for this system, and no
// switch turned downloads off (DownloadDisabledFor).
func PlanFor(t Target) (Plan, error) {
	f, err := FindFor(t)
	if err == nil {
		return Plan{Found: &f}, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Plan{}, err
	}
	if _, ok := Pinned(); !ok {
		return Plan{Why: ErrUnsupported.Error()}, nil
	}
	if off, by := DownloadDisabledFor(t); off {
		return Plan{Why: "downloading ffmpeg is switched off (" + by + ")"}, nil
	}
	return Plan{Install: true}, nil
}

// PrepareFor makes t's copy directory safe to look at (Target.Secure) and
// then decides what to do (PlanFor). note is what Secure reports.
func PrepareFor(t Target) (p Plan, note string, err error) {
	if t.Secure != nil {
		if note, err = t.Secure(t.CopyDir(), false); err != nil {
			return Plan{}, note, err
		}
	}
	p, err = PlanFor(t)
	return p, note, err
}

// InstallFor installs RemoHop's copy for t, in its directory, exactly as
// Install does for this user: the same pinned build, the same checks.
// Call PrepareFor first. Downloads switched off for t (DownloadDisabledFor)
// are refused. It returns the ffmpeg t uses afterwards.
func InstallFor(ctx context.Context, t Target, o Options) (Found, error) {
	if off, by := DownloadDisabledFor(t); off {
		return Found{}, fmt.Errorf("downloading ffmpeg is switched off (%s)", by)
	}
	o.Dir = t.CopyDir()
	if err := os.MkdirAll(o.Dir, 0o700); err != nil {
		return Found{}, err
	}
	if _, err := Install(ctx, o); err != nil {
		return Found{}, err
	}
	if t.Secure != nil {
		if _, err := t.Secure(o.Dir, true); err != nil {
			return Found{}, err
		}
	}
	return FindFor(t)
}

// ManifestFor describes t's copy, if it has one.
func ManifestFor(t Target) (Manifest, bool) {
	if fi, err := os.Stat(filepath.Join(t.CopyDir(), exeName())); err != nil || !fi.Mode().IsRegular() {
		return Manifest{}, false
	}
	return manifestIn(t.CopyDir())
}
