package service

import (
	"slices"
	"strings"
	"testing"
)

func TestWindowsRunArgsUnattended(t *testing.T) {
	w := winService{}
	if args := w.runArgs(Options{NoSignal: true}); slices.Contains(args, "--unattended") {
		t.Errorf("unattended without asking: %v", args)
	}
	args := w.runArgs(Options{NoSignal: true, ConfigDir: `C:\state`, Unattended: true})
	want := []string{"service", "run", "--no-signal", "--config-dir", `C:\state`, "--unattended"}
	if !slices.Equal(args, want) {
		t.Errorf("runArgs = %v, want %v", args, want)
	}
	text, err := w.Print(Options{Exe: `C:\Program Files\RemoHop\remohop.exe`, Unattended: true})
	if err != nil || !strings.Contains(text, "--unattended") || !strings.Contains(text, "unattended access") {
		t.Errorf("print: %v\n%s", err, text)
	}
	t.Setenv("ProgramFiles", `C:\Program Files`)
	if !adminOnlyLocation(`C:\Program Files\RemoHop\remohop.exe`) || adminOnlyLocation(`C:\Users\me\Downloads\remohop.exe`) {
		t.Error("adminOnlyLocation")
	}
	if notes := unattendedNotes(`C:\Users\me\remohop.exe`); !strings.Contains(strings.Join(notes, "\n"), "only administrators") {
		t.Errorf("no warning about a user-writable binary: %q", notes)
	}
}
