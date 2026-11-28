// Package ffmpeg finds the ffmpeg program that screen sharing runs, and on
// Windows and macOS installs a verified build of it when there is none.
//
// Which ffmpeg is used (Find): the one $REMOHOP_FFMPEG names, else one on
// PATH (the system's), else RemoHop's own copy. RemoHop's copy is only ever
// downloaded when someone asks for it (`remohop ffmpeg install`, the button
// in the desktop app and the quick support window, or turning unattended
// access on in the desktop app on Windows, for the service: target.go),
// never by itself. The download is one of the builds pinned in builds.go: a fixed,
// versioned file whose SHA-256 is in this source. It is fetched over HTTPS
// (through the proxy that HTTPS_PROXY names, if any), checked against the
// pin before anything in it is read, and only the ffmpeg program is taken
// out of it, into this user's RemoHop directory. REMOHOP_FFMPEG_DOWNLOAD=off
// (or the setting ffmpeg-download off) switches downloads off.
//
// The builds are GPL builds. Screen sharing prefers hardware encoders, which
// LGPL builds have too, but falls back to libx264 where there is no working
// one (a virtual machine, a server without a GPU): it is much faster than
// the encoders an LGPL build has (OpenH264, software AV1). RemoHop runs
// ffmpeg as a separate program and does not ship it: the user's computer
// downloads it from its publisher.
//
// Linux is left to its distribution's ffmpeg.
package ffmpeg

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/nowindow"
)

// Environment variables and the setting.
const (
	// PathEnv names the ffmpeg program to use, whatever else there is.
	PathEnv = "REMOHOP_FFMPEG"
	// DownloadEnv set to off switches downloads off (for managed
	// computers); it wins over the setting.
	DownloadEnv = "REMOHOP_FFMPEG_DOWNLOAD"
	// SettingDownload is the same as a setting (`remohop config`).
	SettingDownload = "ffmpeg-download"
)

func init() {
	config.RegisterSetting(config.Setting{Key: SettingDownload, Default: "on", Validate: config.ValidateOnOff,
		Help: "whether RemoHop may download a verified ffmpeg for screen sharing when asked (Windows, macOS); " + DownloadEnv + "=off wins"})
}

// Kind says where the ffmpeg in use comes from.
type Kind string

const (
	KindEnv     Kind = "env"     // named by REMOHOP_FFMPEG
	KindSystem  Kind = "system"  // found on PATH
	KindRemoHop Kind = "remohop" // RemoHop's downloaded copy
)

// Describe says it for people.
func (k Kind) Describe() string {
	switch k {
	case KindEnv:
		return "named by " + PathEnv
	case KindSystem:
		return "the system's, on PATH"
	case KindRemoHop:
		return "RemoHop's copy"
	}
	return string(k)
}

// Found is the ffmpeg in use.
type Found struct {
	Path string `json:"path"`
	Kind Kind   `json:"kind"`
}

// ErrNotFound: there is no ffmpeg.
var ErrNotFound = errors.New("ffmpeg not found")

// Find is the ffmpeg screen sharing uses: $REMOHOP_FFMPEG (as it is, as
// before), else one on PATH, else RemoHop's copy.
func Find() (Found, error) {
	if p := os.Getenv(PathEnv); p != "" {
		return Found{Path: p, Kind: KindEnv}, nil
	}
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return Found{Path: p, Kind: KindSystem}, nil
	}
	if p, ok := installed(); ok {
		return Found{Path: p, Kind: KindRemoHop}, nil
	}
	return Found{}, ErrNotFound
}

// Pinned is the build RemoHop downloads on this system, if any.
func Pinned() (Build, bool) { return pinned() }

// pinned is Pinned's answer (tests pretend to be another system).
var pinned = func() (Build, bool) { return BuildFor(runtime.GOOS, runtime.GOARCH) }

// installed is RemoHop's copy, when this system has a pinned build and
// the copy is there.
func installed() (string, bool) {
	if _, ok := Pinned(); !ok {
		return "", false
	}
	p, err := path()
	if err != nil {
		return "", false
	}
	if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() || !trustedCopy(p) {
		return "", false
	}
	return p, true
}

// Dir is where RemoHop's copy lives: "ffmpeg" in RemoHop's directory
// ($REMOHOP_CONFIG_DIR, as the Windows service and tests have it), except
// on Windows where it goes to the local (not roaming) application data
// directory, %LocalAppData%\remohop\ffmpeg: a 100 MB program has no place in
// a roaming profile.
func Dir() (string, error) {
	if os.Getenv("REMOHOP_CONFIG_DIR") != "" || runtime.GOOS != "windows" {
		return config.SubDir("ffmpeg")
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	d := filepath.Join(base, "remohop", "ffmpeg")
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, nil
}

func exeName() string {
	if runtime.GOOS == "windows" {
		return "ffmpeg.exe"
	}
	return "ffmpeg"
}

// path is where RemoHop's copy is (or would be).
func path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, exeName()), nil
}

// DownloadDisabled reports whether downloads are switched off, and by what
// (the environment variable or the setting).
func DownloadDisabled() (bool, string) {
	if v := strings.TrimSpace(os.Getenv(DownloadEnv)); v != "" {
		if envOff(v) {
			return true, DownloadEnv + "=" + v
		}
		return false, ""
	}
	if v, err := config.Get(SettingDownload); err == nil && v != "" && !config.Bool(v, true) {
		return true, "the setting " + SettingDownload + " (" + v + ")"
	}
	return false, ""
}

// envOff reports whether a value of DownloadEnv switches downloads off.
func envOff(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "0", "false", "no":
		return true
	}
	return false
}

// InstallHint is the sentence that offers `remohop ffmpeg install`, or ""
// where it cannot be used (no pinned build, downloads switched off).
func InstallHint() string {
	b, ok := Pinned()
	if !ok {
		return ""
	}
	if off, _ := DownloadDisabled(); off {
		return ""
	}
	return fmt.Sprintf("Run `remohop ffmpeg install` to download a verified build (about %d MB)", b.SizeMB())
}

// Version is the first line of `ffmpeg -version` without its copyright
// ("ffmpeg version 9.1-essentials_build-www.gyan.dev"), or "" when it does
// not run.
func Version(ctx context.Context, ffmpeg string) string {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := nowindow.Set(exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-version")).Output()
	if err != nil {
		return ""
	}
	line, _, _ := bufio.NewReader(bytes.NewReader(out)).ReadLine()
	v, _, _ := strings.Cut(string(line), " Copyright")
	return strings.TrimSpace(v)
}

// removeIfExists removes p, ignoring a missing file.
func removeIfExists(p string) error {
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
