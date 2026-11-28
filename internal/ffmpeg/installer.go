package ffmpeg

import (
	"context"
	"errors"
	"sync"
	"time"
)

// UIState is ffmpeg as the desktop app and the quick support window show
// it: whether screen sharing lacks it, and the one-click install.
type UIState struct {
	// Missing: no ffmpeg was found, so screen sharing cannot work.
	Missing bool `json:"missing"`
	// CanInstall: RemoHop can download a verified build here (a pinned
	// build for this system, downloads not switched off).
	CanInstall bool `json:"can_install"`
	// Disabled names what switched downloads off, if anything did.
	Disabled string `json:"disabled,omitempty"`
	// SizeMB is the download's size.
	SizeMB  int    `json:"size_mb,omitempty"`
	Version string `json:"version,omitempty"`
	Source  string `json:"source,omitempty"`
	// Stage is the install under way: downloading (Percent), verifying,
	// installing.
	Stage   string `json:"stage,omitempty"`
	Percent int    `json:"percent"`
	// Error is the last install's failure.
	Error string `json:"error,omitempty"`
}

// Installer runs the one-click install for a UI: one at a time, in the
// background, with its progress in State. The zero value works.
type Installer struct {
	// OnChange, if set, is called when State changes.
	OnChange func()

	// For tests: what finds ffmpeg, installs it, and the pinned build.
	Find    func() (Found, error)
	Install func(ctx context.Context, progress Progress) (string, error)
	Pinned  func() (Build, bool)

	mu      sync.Mutex
	running bool
	stage   string
	percent int
	err     string
	missing bool
	checked time.Time
}

// recheck is how long a "found" or "missing" answer is kept: State is
// read at every change of the page's state, and ffmpeg installed by other
// means (winget, brew) is noticed within this time.
const recheck = 10 * time.Second

func (in *Installer) find() (Found, error) {
	if in.Find != nil {
		return in.Find()
	}
	return Find()
}

func (in *Installer) pinned() (Build, bool) {
	if in.Pinned != nil {
		return in.Pinned()
	}
	return Pinned()
}

// State reports ffmpeg's state for the page.
func (in *Installer) State() UIState {
	in.mu.Lock()
	if in.checked.IsZero() || time.Since(in.checked) >= recheck {
		_, err := in.find()
		in.missing, in.checked = err != nil, time.Now()
	}
	st := UIState{Missing: in.missing, Stage: in.stage, Percent: in.percent, Error: in.err}
	in.mu.Unlock()
	if !st.Missing && st.Stage == "" {
		return UIState{}
	}
	if b, ok := in.pinned(); ok {
		st.SizeMB, st.Version, st.Source = b.SizeMB(), b.Version, b.Source
		off, by := DownloadDisabled()
		st.CanInstall, st.Disabled = !off, by
	}
	return st
}

// Refresh forgets the last answer, so that the next State looks for ffmpeg
// again.
func (in *Installer) Refresh() {
	in.mu.Lock()
	in.checked = time.Time{}
	in.mu.Unlock()
}

// ErrBusy: an install is already under way.
var ErrBusy = errors.New("ffmpeg is already being installed")

// Start begins an install in the background; State follows it. ctx ends
// it (the app quitting).
func (in *Installer) Start(ctx context.Context) error {
	st := in.State()
	if !st.CanInstall {
		if st.Disabled != "" {
			return errors.New("downloading ffmpeg is switched off (" + st.Disabled + ")")
		}
		if !st.Missing {
			return errors.New("ffmpeg is already installed")
		}
		return ErrUnsupported
	}
	in.mu.Lock()
	if in.running {
		in.mu.Unlock()
		return ErrBusy
	}
	in.running, in.stage, in.percent, in.err = true, "downloading", 0, ""
	in.mu.Unlock()
	in.changed()
	install := in.Install
	if install == nil {
		install = func(ctx context.Context, p Progress) (string, error) { return Install(ctx, Options{Progress: p}) }
	}
	go func() {
		_, err := install(ctx, in.progress)
		in.mu.Lock()
		in.running, in.stage, in.percent = false, "", 0
		if err != nil {
			in.err = err.Error()
		}
		in.checked = time.Time{} // look again
		in.mu.Unlock()
		in.changed()
	}()
	return nil
}

func (in *Installer) progress(stage string, done, total int64) {
	pct := 0
	if total > 0 {
		pct = int(done * 100 / total)
	}
	in.mu.Lock()
	same := in.stage == stage && in.percent == pct
	in.stage, in.percent = stage, pct
	in.mu.Unlock()
	if !same {
		in.changed()
	}
}

func (in *Installer) changed() {
	if in.OnChange != nil {
		in.OnChange()
	}
}
