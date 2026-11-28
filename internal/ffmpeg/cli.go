package ffmpeg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"

	"github.com/readori/RemoHop/internal/cli"
)

const usage = `Usage: remohop ffmpeg [status] [--json]
       remohop ffmpeg install [--force] [--service]
       remohop ffmpeg remove

Screen sharing (remote desktop, quick support) runs ffmpeg on the computer
whose screen is shared. RemoHop uses, in this order: the program that
REMOHOP_FFMPEG names; ffmpeg on PATH (the system's); RemoHop's own copy.

  status    which ffmpeg screen sharing uses, and its version
  install   download RemoHop's copy (Windows and macOS): a build pinned in
            RemoHop, checked against its SHA-256 before it is unpacked, into
            this user's RemoHop directory. --force downloads it again.
            --service: for the Windows service with unattended access
            instead, into its directory (%ProgramData%\RemoHop\ffmpeg),
            which only SYSTEM and the Administrators can change. Run it from
            an elevated prompt. The desktop app does this when it turns
            unattended access on.
  remove    delete RemoHop's copy

Builds: Windows x64 from gyan.dev, Windows arm64 from BtbN/FFmpeg-Builds,
macOS (Apple silicon and Intel) from ffmpeg.martin-riedl.de; all GPL builds.
On Linux install ffmpeg with the package manager.

The download goes through the proxy that HTTPS_PROXY names, if any.
REMOHOP_FFMPEG_DOWNLOAD=off, or ` + "`remohop config set ffmpeg-download off`" + `,
switches downloads off (for managed computers). For the service, its own
setting and environment count too: off in any of them switches it off.`

func init() {
	cli.Register(&cli.Command{
		Name:    "ffmpeg",
		Summary: "show or install the ffmpeg that screen sharing uses",
		Usage:   usage,
		Run:     run,
	})
}

func run(ctx context.Context, env *cli.Env, args []string) int {
	sub := "status"
	if len(args) > 0 && (args[0] == "status" || args[0] == "install" || args[0] == "remove") {
		sub, args = args[0], args[1:]
	}
	fs := cli.NewFlagSet(env, "ffmpeg "+sub, usage)
	asJSON := fs.Bool("json", false, "")
	force := fs.Bool("force", false, "")
	forService := fs.Bool("service", false, "")
	if err := fs.Parse(args); err != nil {
		return cli.FlagExit(err)
	}
	if fs.NArg() != 0 {
		env.Errorf("ffmpeg: unexpected argument %q", fs.Arg(0))
		return cli.ExitUsage
	}
	if *forService && sub != "install" {
		env.Errorf("ffmpeg: --service goes with install")
		return cli.ExitUsage
	}
	switch sub {
	case "install":
		if *forService {
			return runInstallService(ctx, env, *force)
		}
		return runInstall(ctx, env, *force)
	case "remove":
		had, err := Remove()
		if err != nil {
			env.Errorf("ffmpeg remove: %v", err)
			return cli.ExitError
		}
		if had {
			fmt.Fprintln(env.Stdout, "removed RemoHop's copy of ffmpeg")
		} else {
			fmt.Fprintln(env.Stdout, "RemoHop has no copy of ffmpeg here")
		}
		return cli.ExitOK
	}
	return runStatus(ctx, env, *asJSON)
}

// Status is what `remohop ffmpeg status --json` prints.
type Status struct {
	Found   *Found `json:"found,omitempty"`
	Version string `json:"version,omitempty"`
	// Installed describes RemoHop's copy, whether in use or not.
	Installed *Manifest `json:"installed,omitempty"`
	// Pinned is the build `install` downloads here, if any.
	Pinned *PinnedInfo `json:"pinned,omitempty"`
	// DownloadOff names what switched downloads off.
	DownloadOff string `json:"download_off,omitempty"`
	// Service is the ffmpeg of the Windows service with unattended
	// access, if there is one.
	Service *ServiceStatus `json:"service,omitempty"`
}

// ServiceStatus is the service's side of the status.
type ServiceStatus struct {
	Name string `json:"name"`
	// Dir is where its copy is (or would be).
	Dir   string `json:"dir"`
	Found *Found `json:"found,omitempty"`
	// Installed describes its copy, if it has one.
	Installed *Manifest `json:"installed,omitempty"`
	// Error: its directory could not be read (it is the administrators'
	// only).
	Error string `json:"error,omitempty"`
}

// PinnedInfo is a Build for the status.
type PinnedInfo struct {
	Version string   `json:"version"`
	Source  string   `json:"source"`
	License string   `json:"license"`
	URLs    []string `json:"urls"`
	SHA256  string   `json:"sha256"`
	SizeMB  int      `json:"size_mb"`
}

func status(ctx context.Context) Status {
	var st Status
	if f, err := Find(); err == nil {
		st.Found = &f
		st.Version = Version(ctx, f.Path)
	}
	if m, ok := InstalledManifest(); ok {
		st.Installed = &m
	}
	if b, ok := Pinned(); ok {
		st.Pinned = &PinnedInfo{Version: b.Version, Source: b.Source, License: b.License, URLs: b.URLs, SHA256: b.SHA256, SizeMB: b.SizeMB()}
	}
	_, st.DownloadOff = DownloadDisabled()
	if Service != nil {
		if t, ok, err := Service(ctx); err == nil && ok {
			ss := &ServiceStatus{Name: t.Name, Dir: t.CopyDir()}
			if f, err := FindFor(t); err == nil {
				ss.Found = &f
			} else if !errors.Is(err, ErrNotFound) {
				ss.Error = err.Error()
			}
			if m, ok := ManifestFor(t); ok {
				ss.Installed = &m
			}
			st.Service = ss
		}
	}
	return st
}

func runStatus(ctx context.Context, env *cli.Env, asJSON bool) int {
	st := status(ctx)
	if asJSON {
		b, _ := json.MarshalIndent(st, "", "  ")
		fmt.Fprintln(env.Stdout, string(b))
		return cli.ExitOK
	}
	w := env.Stdout
	if st.Found == nil {
		fmt.Fprintln(w, "ffmpeg:   not found (screen sharing needs it; screenshots do not)")
	} else {
		fmt.Fprintf(w, "ffmpeg:   %s (%s)\n", st.Found.Path, st.Found.Kind.Describe())
		v := st.Version
		if v == "" {
			v = "unknown: it did not run"
		}
		fmt.Fprintf(w, "version:  %s\n", v)
	}
	if m := st.Installed; m != nil {
		fmt.Fprintf(w, "RemoHop's copy: ffmpeg %s from %s, installed %s\n", m.Version, m.Source, m.Installed.Local().Format("2006-01-02"))
		if st.Pinned != nil && m.SHA256 != st.Pinned.SHA256 {
			fmt.Fprintf(w, "  this version of RemoHop pins ffmpeg %s instead: `remohop ffmpeg install` replaces it\n", st.Pinned.Version)
		}
	}
	switch {
	case st.Pinned == nil:
		if st.Found == nil {
			fmt.Fprintf(w, "RemoHop has no ffmpeg build to download for %s/%s: install ffmpeg with the package manager.\n", runtime.GOOS, runtime.GOARCH)
		}
	case st.DownloadOff != "":
		fmt.Fprintf(w, "downloads: switched off by %s\n", st.DownloadOff)
	case st.Found == nil:
		fmt.Fprintf(w, "`remohop ffmpeg install` downloads ffmpeg %s from %s (%s, about %d MB).\n",
			st.Pinned.Version, st.Pinned.Source, st.Pinned.License, st.Pinned.SizeMB)
	}
	if ss := st.Service; ss != nil {
		switch {
		case ss.Found != nil:
			fmt.Fprintf(w, "service:  %s (%s), for %s\n", ss.Found.Path, ss.Found.Kind.Describe(), ss.Name)
			if m := ss.Installed; m != nil && ss.Found.Kind == KindRemoHop {
				fmt.Fprintf(w, "  ffmpeg %s from %s, installed %s\n", m.Version, m.Source, m.Installed.Local().Format("2006-01-02"))
			}
		case ss.Error != "":
			fmt.Fprintf(w, "service:  cannot tell what %s uses without an administrator: %s\n", ss.Name, ss.Error)
		default:
			fmt.Fprintf(w, "service:  not found: %s cannot share the screen; from an elevated prompt, `remohop ffmpeg install --service`\n", ss.Name)
		}
	}
	return cli.ExitOK
}

func runInstall(ctx context.Context, env *cli.Env, force bool) int {
	b, ok := Pinned()
	if !ok {
		env.Errorf("ffmpeg install: %v", ErrUnsupported)
		return cli.ExitError
	}
	if off, by := DownloadDisabled(); off {
		env.Errorf("ffmpeg install: downloading ffmpeg is switched off (%s)", by)
		return cli.ExitError
	}
	if f, err := Find(); err == nil && f.Kind != KindRemoHop && !force {
		fmt.Fprintf(env.Stdout, "screen sharing uses %s (%s): nothing to install. "+
			"--force installs RemoHop's copy anyway, for when that one is gone.\n", f.Path, f.Kind.Describe())
		return cli.ExitOK
	}
	fmt.Fprintf(env.Stderr, "downloading ffmpeg %s from %s (%s, about %d MB)\n", b.Version, b.Source, b.License, b.SizeMB())
	p, err := Install(ctx, Options{Force: force, Progress: printProgress(env)})
	if err != nil {
		env.Errorf("ffmpeg install: %v", err)
		return cli.ExitError
	}
	fmt.Fprintf(env.Stdout, "installed %s (SHA-256 of the download checked)\n", p)
	return cli.ExitOK
}

// printProgress shows an install's progress on stderr.
func printProgress(env *cli.Env) Progress {
	lastPct, lastStage := -1, ""
	return func(stage string, done, total int64) {
		if stage == "downloading" && total > 0 {
			// Every tenth.
			if pct := int(done*10/total) * 10; pct != lastPct {
				lastPct = pct
				fmt.Fprintf(env.Stderr, "  %d%%\n", pct)
			}
			return
		}
		if stage != lastStage && stage != "downloading" {
			fmt.Fprintf(env.Stderr, "  %s\n", stage)
		}
		lastStage = stage
	}
}

// runInstallService is `install --service`: RemoHop's copy for the Windows
// service, where only SYSTEM and the Administrators can change it.
func runInstallService(ctx context.Context, env *cli.Env, force bool) int {
	if Service == nil {
		env.Errorf("ffmpeg install --service: only the Windows service has a directory of its own; on %s the service uses this user's ffmpeg", runtime.GOOS)
		return cli.ExitError
	}
	t, ok, err := Service(ctx)
	if err != nil {
		env.Errorf("ffmpeg install --service: %v", err)
		return cli.ExitError
	}
	if !ok {
		env.Errorf("ffmpeg install --service: the RemoHop service is not installed with unattended access (remohop service install --unattended)")
		return cli.ExitError
	}
	b, ok := Pinned()
	if !ok {
		env.Errorf("ffmpeg install --service: %v", ErrUnsupported)
		return cli.ExitError
	}
	plan, note, err := PrepareFor(t)
	if note != "" {
		fmt.Fprintln(env.Stderr, note)
	}
	if err != nil {
		env.Errorf("ffmpeg install --service: %v (run it from an elevated prompt)", err)
		return cli.ExitError
	}
	if f := plan.Found; f != nil && !force {
		again := "--force installs RemoHop's copy anyway."
		if f.Kind == KindRemoHop {
			again = "--force downloads it again."
		}
		fmt.Fprintf(env.Stdout, "%s uses %s (%s): nothing to install. %s\n", t.Name, f.Path, f.Kind.Describe(), again)
		return cli.ExitOK
	}
	if off, by := DownloadDisabledFor(t); off {
		env.Errorf("ffmpeg install --service: downloading ffmpeg is switched off (%s)", by)
		return cli.ExitError
	}
	fmt.Fprintf(env.Stderr, "downloading ffmpeg %s from %s (%s, about %d MB) for %s\n", b.Version, b.Source, b.License, b.SizeMB(), t.Name)
	f, err := InstallFor(ctx, t, Options{Force: force, Progress: printProgress(env)})
	if err != nil {
		env.Errorf("ffmpeg install --service: %v", err)
		return cli.ExitError
	}
	fmt.Fprintf(env.Stdout, "%s uses %s (SHA-256 of the download checked; only SYSTEM and the Administrators can change it)\n", t.Name, f.Path)
	return cli.ExitOK
}
