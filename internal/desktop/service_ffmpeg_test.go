//go:build !windows

package desktop

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/ffmpeg"
)

// fakeServiceFFmpeg stands in for the Windows service: its directory, and
// a PATH of its own (path, if not empty).
func fakeServiceFFmpeg(t *testing.T, path string) ffmpeg.Target {
	t.Helper()
	tg := ffmpeg.Target{Name: "the test service", ConfigDir: t.TempDir(),
		Getenv: func(k string) string {
			if k == "PATH" {
				return path
			}
			return ""
		}}
	old := ffmpeg.Service
	ffmpeg.Service = func(context.Context) (ffmpeg.Target, bool, error) { return tg, true, nil }
	t.Cleanup(func() { ffmpeg.Service = old })
	return tg
}

func TestNeedServiceFFmpeg(t *testing.T) {
	for _, c := range []struct {
		st    UnattendedState
		known string
		want  bool
	}{
		{UnattendedState{}, "", false},                                    // no service
		{UnattendedState{Installed: true}, "", true},                      // nobody looked, or it has none
		{UnattendedState{Installed: true}, `C:\ffmpeg\ffmpeg.exe`, false}, // it has one
		{UnattendedState{Installed: true, Busy: true}, "", false},         // a step is under way
	} {
		if got := needServiceFFmpeg(c.st, c.known); got != c.want {
			t.Errorf("needServiceFFmpeg(%+v, %q) = %v", c.st, c.known, got)
		}
	}
}

// TestServiceFFmpegStep: the administrator step "ffmpeg", run in-process
// in place of the elevated one: it reports the service's ffmpeg, which the
// app remembers; a service without one is a problem to show, and so is a
// step that failed.
func TestServiceFFmpegStep(t *testing.T) {
	app, _ := startApp(t, "")
	old := runElevated
	t.Cleanup(func() { runElevated = old })
	runElevated = func(ctx context.Context, exe string, args []string) error {
		var out, errb bytes.Buffer
		runUnattendedHelper(ctx, &cli.Env{Stdin: strings.NewReader(""), Stdout: &out, Stderr: &errb}, args[1:])
		return nil
	}

	// It has ffmpeg on its PATH: nothing to install, and remembered.
	sys := t.TempDir()
	ff := filepath.Join(sys, "ffmpeg")
	if err := os.WriteFile(ff, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeServiceFFmpeg(t, sys)
	r, err := app.elevatedResult(context.Background(), "ffmpeg", "")
	if err != nil || r.FFmpeg == nil || r.FFmpeg.Found == nil || r.FFmpeg.Found.Path != ff || r.More {
		t.Fatalf("result: %+v %v", r, err)
	}
	if problem := app.serviceFFmpegResult(r, nil); problem != nil {
		t.Errorf("a service with ffmpeg is a problem: %v", problem)
	}
	app.mu.Lock()
	known := app.settings.ServiceFFmpeg
	app.mu.Unlock()
	if known != ff {
		t.Errorf("remembered %q", known)
	}

	// It has none, and none can be installed here (no build for Linux).
	fakeServiceFFmpeg(t, "")
	r, err = app.elevatedResult(context.Background(), "ffmpeg", "")
	if err != nil || r.FFmpeg == nil || r.FFmpeg.Found != nil || r.FFmpeg.Install || r.FFmpeg.Why == "" {
		t.Fatalf("result: %+v %v", r, err)
	}
	if problem := app.serviceFFmpegResult(r, nil); problem == nil || !strings.Contains(problem.Error(), "cannot share the screen") {
		t.Errorf("problem: %v", problem)
	}
	app.mu.Lock()
	known = app.settings.ServiceFFmpeg
	app.mu.Unlock()
	if known != "" {
		t.Errorf("still remembered %q", known)
	}
	if problem := app.serviceFFmpegResult(elevatedResult{}, errors.New("the administrator prompt was cancelled")); problem == nil || !strings.Contains(problem.Error(), "cancelled") {
		t.Errorf("a failed step: %v", problem)
	}
	// No service: nothing to say.
	ffmpeg.Service = func(context.Context) (ffmpeg.Target, bool, error) { return ffmpeg.Target{}, false, nil }
	r, err = app.elevatedResult(context.Background(), "ffmpeg", "")
	if err != nil || r.FFmpeg != nil || app.serviceFFmpegResult(r, nil) != nil {
		t.Errorf("no service: %+v %v", r, err)
	}
}

// TestElevatedStepMore: a step that reports "more" (unattended access is on,
// ffmpeg for the service is being downloaded) lets the app go on at once;
// its final result comes later.
func TestElevatedStepMore(t *testing.T) {
	app, _ := startApp(t, "")
	old := runElevated
	t.Cleanup(func() { runElevated = old })
	release := make(chan struct{})
	runElevated = func(ctx context.Context, exe string, args []string) error {
		result := ""
		for i, a := range args {
			if a == "--result" {
				result = args[i+1]
			}
		}
		writeElevatedResult(result, elevatedResult{ID: "123", More: true})
		<-release
		return writeElevatedResult(result, elevatedResult{ID: "123", FFmpeg: &ffmpeg.Plan{Found: &ffmpeg.Found{Path: "/x/ffmpeg", Kind: ffmpeg.KindRemoHop}}})
	}
	final := make(chan elevatedResult, 1)
	start := time.Now()
	r, err := app.elevatedStep(context.Background(), "enable", "", func(r elevatedResult, err error) {
		if err != nil {
			t.Error(err)
		}
		final <- r
	})
	if err != nil || !r.More || r.ID != "123" {
		t.Fatalf("first result: %+v %v", r, err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("waited %v for the first result", time.Since(start))
	}
	close(release)
	select {
	case r := <-final:
		if r.More || r.FFmpeg == nil || r.FFmpeg.Found == nil || r.FFmpeg.Found.Path != "/x/ffmpeg" {
			t.Errorf("final result: %+v", r)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no final result")
	}
}
