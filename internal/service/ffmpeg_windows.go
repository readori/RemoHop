package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"

	"github.com/readori/RemoHop/internal/ffmpeg"
)

// The service with unattended access shares the screen, so it needs ffmpeg
// of its own: see internal/ffmpeg (target.go). This tells that package
// where the service is, what its environment is, and how to keep its copy
// out of everyone else's reach.
func init() { ffmpeg.Service = ffmpegTarget }

func ffmpegTarget(ctx context.Context) (ffmpeg.Target, bool, error) {
	st, err := winService{}.Status(ctx, Options{})
	if err != nil {
		return ffmpeg.Target{}, false, err
	}
	if !st.Installed || !st.Unattended {
		return ffmpeg.Target{}, false, nil
	}
	stateDir := st.ConfigDir
	return ffmpeg.Target{
		Name:      "the RemoHop service",
		ConfigDir: stateDir,
		Getenv:    serviceEnv(st.Name),
		Secure: func(dir string, installed bool) (string, error) {
			return secureProgramDir(stateDir, dir, installed)
		},
	}, true, nil
}

// serviceEnv reads the environment the service named name starts with, as
// far as finding ffmpeg goes: the machine's environment variables (not a
// user's), with the service's own Environment value over them. (Windows
// gives services a changed machine PATH only after it restarts; this reads
// the PATH as it is now.)
func serviceEnv(name string) func(string) string {
	env := map[string]string{}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`, registry.QUERY_VALUE); err == nil {
		names, _ := k.ReadValueNames(0)
		for _, n := range names {
			v, typ, err := k.GetStringValue(n)
			if err != nil {
				continue
			}
			if typ == registry.EXPAND_SZ {
				if x, err := registry.ExpandString(v); err == nil {
					v = x
				}
			}
			env[strings.ToUpper(n)] = v
		}
		k.Close()
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\`+name, registry.QUERY_VALUE); err == nil {
		if vs, _, err := k.GetStringsValue("Environment"); err == nil {
			for _, kv := range vs {
				if n, v, ok := strings.Cut(kv, "="); ok && n != "" {
					env[strings.ToUpper(n)] = v
				}
			}
		}
		k.Close()
	}
	return func(k string) string { return env[strings.ToUpper(k)] }
}

// secureProgramDir prepares dir, a directory right in the service's state
// directory stateDir that holds a program the service runs (RemoHop's copy
// of ffmpeg). It runs as an administrator (`remohop ffmpeg install
// --service`, the desktop app's administrator step), before the directory
// is looked at and again after an install into it.
//
// The state directory must be one the service trusts (as adoptStateDir
// checks it), and its access list is set to SYSTEM and the Administrators
// only once more: a step that wrote the service's settings may have let
// the administrator's own account in (config.MakePrivate), and with it
// that account's programs that run without elevation. Then dir and
// everything in it must belong to SYSTEM, the Administrators or
// TrustedInstaller, not to the administrator running this (whose
// unelevated programs could have put it there), except right after this
// process installed into it (installed), and must contain no links,
// junctions or hard links. A directory that fails is moved aside, kept for
// inspection, and a new one made. What is left belongs to the
// Administrators, and only SYSTEM and the Administrators may access it.
func secureProgramDir(stateDir, dir string, installed bool) (note string, err error) {
	if !strings.EqualFold(filepath.Clean(filepath.Dir(dir)), filepath.Clean(stateDir)) {
		return "", fmt.Errorf("%s is not in the service's state directory %s", dir, stateDir)
	}
	h, info, err := openNoFollow(stateDir, windows.READ_CONTROL)
	if err != nil {
		return "", fmt.Errorf("cannot open the service's state directory %s: %w", stateDir, err)
	}
	owner, oerr := ownerOf(h)
	windows.CloseHandle(h)
	switch {
	case info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0:
		return "", fmt.Errorf("the service's state directory %s is a link or junction", stateDir)
	case info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0:
		return "", fmt.Errorf("the service's state directory %s is not a directory", stateDir)
	case oerr != nil:
		return "", oerr
	case !trustedOwner(owner, nil):
		return "", fmt.Errorf("the service's state directory %s belongs to %s", stateDir, accountName(owner))
	}
	if err := secureDir(stateDir); err != nil {
		return "", fmt.Errorf("cannot restrict access to %s: %w", stateDir, err)
	}
	var me *windows.SID
	if installed {
		if me, err = currentUserSID(); err != nil {
			return "", err
		}
	}
	if _, err := os.Lstat(dir); err == nil {
		why, err := lockTree(dir, me)
		if why == "" {
			return "", err
		}
		aside := fmt.Sprintf("%s.untrusted-%d", dir, time.Now().Unix())
		if err := os.Rename(dir, aside); err != nil {
			return "", fmt.Errorf("%s cannot be trusted (%s) and cannot be moved aside: %w", dir, why, err)
		}
		note = fmt.Sprintf("remohop: %s could not be trusted (%s); it was moved to %s", dir, why, aside)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return note, err
	}
	return note, secureDir(dir)
}
