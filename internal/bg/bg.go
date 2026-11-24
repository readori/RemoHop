// Package bg runs long-lived client-side commands (port forwards, FUSE
// mounts, clipboard sync) in the background and keeps a registry of them so
// `remohop ps` and `remohop stop` can find them again.
//
// A command that supports -b/--background does:
//
//	if background && !bg.InChild() {
//		msg, pid, err := bg.Start(30 * time.Second)
//		// print msg, return
//	}
//	... establish the forward / mount ...
//	done := bg.Ready(bg.Info{Kind: "forward", Target: env.Target, Detail: "..."}, "forwarding ...")
//	defer done()
//	<-ctx.Done()
//
// Start re-executes the current binary with identical arguments, detached
// from the terminal, and blocks until that child calls Ready (or dies).
// Ready also works in the foreground, where it only records the process.
package bg

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/config"
)

const (
	envReady = "REMOHOP_BG_READY" // path the child writes its ready message to
	envLog   = "REMOHOP_BG_LOG"   // path of the child's log file
)

// Info describes one running background (or foreground long-lived) command.
type Info struct {
	Kind    string    `json:"kind"`   // "forward", "mount", "clip", ...
	Target  string    `json:"target"` // remote peer
	Detail  string    `json:"detail"` // human-readable, e.g. "127.0.0.1:18080 -> 8080"
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
	Log     string    `json:"log,omitempty"`
}

// InChild reports whether this process is the detached child of Start.
func InChild() bool { return os.Getenv(envReady) != "" }

// Start re-executes the running command in the background and waits up to
// timeout for it to call Ready. It returns the child's ready message and
// PID. If the child exits first, the error carries the tail of its log.
func Start(timeout time.Duration) (msg string, pid int, err error) {
	exe, err := os.Executable()
	if err != nil {
		return "", 0, err
	}
	dir, err := config.SubDir("run")
	if err != nil {
		return "", 0, err
	}
	stamp := strconv.FormatInt(time.Now().UnixNano(), 36)
	ready := filepath.Join(dir, "ready-"+stamp)
	logPath := filepath.Join(dir, "bg-"+stamp+".log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return "", 0, err
	}
	defer logf.Close()

	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), envReady+"="+ready, envLog+"="+logPath)
	cmd.Stdin = nil
	cmd.Stdout = logf
	cmd.Stderr = logf
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return "", 0, err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	defer os.Remove(ready)

	deadline := time.After(timeout)
	tick := time.NewTicker(40 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case werr := <-exited:
			// The child may have signalled readiness and then died, or
			// never got that far; either way it is not running.
			return "", 0, fmt.Errorf("background process exited: %v%s", werr, takeLog(logPath))
		case <-deadline:
			cmd.Process.Kill()
			return "", 0, fmt.Errorf("background process not ready after %s%s", timeout, takeLog(logPath))
		case <-tick.C:
			b, rerr := os.ReadFile(ready)
			if rerr != nil || !strings.HasSuffix(string(b), "\n") {
				continue
			}
			// The child is left running; the Wait goroutine above only
			// matters while this (short-lived) parent is still around.
			return strings.TrimSuffix(string(b), "\n"), cmd.Process.Pid, nil
		}
	}
}

// takeLog returns the tail of a failed child's log for the error message and
// removes the file: nothing else will ever read it.
func takeLog(p string) string {
	b, err := os.ReadFile(p)
	os.Remove(p)
	if err != nil || len(b) == 0 {
		return ""
	}
	if len(b) > 2000 {
		b = b[len(b)-2000:]
	}
	return "\n" + strings.TrimRight(string(b), "\n")
}

// Ready records this process in the registry and, when running as the child
// of Start, releases the waiting parent with msg. The returned function
// removes the record; defer it.
func Ready(info Info, msg string) (done func()) {
	info.PID = os.Getpid()
	if info.Started.IsZero() {
		info.Started = time.Now()
	}
	info.Log = os.Getenv(envLog)
	var rec string
	if dir, err := config.SubDir("run"); err == nil {
		rec = filepath.Join(dir, fmt.Sprintf("%d.json", info.PID))
		if b, err := json.Marshal(info); err == nil {
			os.WriteFile(rec, b, 0o600)
		}
	}
	if p := os.Getenv(envReady); p != "" {
		// Write then rename so the parent never reads a partial message.
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, []byte(msg+"\n"), 0o600); err == nil {
			os.Rename(tmp, p)
		}
	}
	return func() {
		if rec != "" {
			os.Remove(rec)
		}
		if info.Log != "" {
			os.Remove(info.Log)
		}
	}
}

// List returns the live recorded processes, oldest first, pruning records
// whose process is gone.
func List() ([]Info, error) {
	dir, err := config.SubDir("run")
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, e := range ents {
		// Records are named <pid>.json. Other commands keep their own
		// sidecar files in the same directory; they are not records.
		pidPart, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok {
			continue
		}
		if _, err := strconv.Atoi(pidPart); err != nil {
			continue
		}
		p := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var in Info
		if json.Unmarshal(b, &in) != nil || in.PID <= 0 || !alive(in.PID) {
			os.Remove(p)
			continue
		}
		out = append(out, in)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out, nil
}

// ErrNotFound is returned by Stop for a PID that is not a recorded process.
var ErrNotFound = errors.New("no such background process")

// Stop asks a recorded process to exit and waits briefly for it to go.
func Stop(pid int) error {
	infos, err := List()
	if err != nil {
		return err
	}
	for _, in := range infos {
		if in.PID != pid {
			continue
		}
		if err := terminate(pid); err != nil {
			return err
		}
		for i := 0; i < 100 && alive(pid); i++ {
			time.Sleep(50 * time.Millisecond)
		}
		if alive(pid) {
			return fmt.Errorf("process %d did not exit", pid)
		}
		return nil
	}
	return ErrNotFound
}
