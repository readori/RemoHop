//go:build !windows

package shell

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSpawnLogsFollowWait(t *testing.T) {
	useJobsDir(t)
	conn := pair(t)
	ctx := testCtx(t)
	j, err := Spawn(ctx, conn, SpawnRequest{
		Command: "echo start; echo to-stderr >&2; sleep 0.5; echo $FOO; pwd; exit 4",
		Name:    "build", Env: []string{"FOO=from-env"}, Cwd: "/",
	})
	if err != nil {
		t.Fatal(err)
	}
	if j.State != JobRunning || j.PID <= 0 || j.Name != "build" || len(j.ID) != 8 {
		t.Fatalf("spawned %+v", j)
	}

	r, err := Logs(ctx, conn, LogsRequest{Job: "build", Follow: true})
	if err != nil {
		t.Fatal(err)
	}
	got := readLogs(t, r)
	r.Close()
	if got != "start\nto-stderr\nfrom-env\n/\n" {
		t.Fatalf("followed log %q", got)
	}
	if fj := r.Job(); fj == nil || fj.State != JobExited || fj.ExitCode != 4 || fj.Ended == nil {
		t.Fatalf("job at end of log: %+v", r.Job())
	}

	w, err := Wait(ctx, conn, j.ID)
	if err != nil || w.ExitCode != 4 || w.State != JobExited {
		t.Fatalf("wait: %+v %v", w, err)
	}

	r, err = Logs(ctx, conn, LogsRequest{Job: j.ID, Tail: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := readLogs(t, r); got != "from-env\n/\n" {
		t.Fatalf("tail 2: %q", got)
	}
	r.Close()

	list, err := Jobs(ctx, conn)
	if err != nil || len(list) != 1 || list[0].ID != j.ID || list[0].LogSize == 0 {
		t.Fatalf("jobs: %+v %v", list, err)
	}
}

func TestKillJobGroup(t *testing.T) {
	useJobsDir(t)
	conn := pair(t)
	ctx := testCtx(t)
	// The job and a child of it; killing the job must take both.
	j, err := Spawn(ctx, conn, SpawnRequest{Command: "sleep 1000 & echo $!; wait"})
	if err != nil {
		t.Fatal(err)
	}
	var childPID int
	waitFor(t, "child pid in the log", 5*time.Second, func() bool {
		b, _ := os.ReadFile(j.Log)
		_, err := fmt.Sscan(string(b), &childPID)
		return err == nil
	})
	if _, err := Kill(ctx, conn, j.ID, "KILL"); err != nil {
		t.Fatal(err)
	}
	w, err := Wait(ctx, conn, j.ID)
	if err != nil || w.State != JobExited || w.Signal != 9 || w.ExitCode != 137 {
		t.Fatalf("after kill: %+v %v", w, err)
	}
	waitFor(t, "job's child to die", 5*time.Second, func() bool { return !procAlive(childPID, 0) })
	if _, err := Kill(ctx, conn, j.ID, ""); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("killing a finished job: %v", err)
	}
	if _, err := Kill(ctx, conn, j.ID, "NOSUCHSIG"); err == nil {
		t.Fatal("bad signal accepted")
	}
}

// A job outlives the client session that spawned it.
func TestJobSurvivesSessionClose(t *testing.T) {
	useJobsDir(t)
	c1, _ := peertestPair(t)
	j, err := Spawn(testCtx(t), c1, SpawnRequest{Command: "sleep 0.5; echo survived"})
	if err != nil {
		t.Fatal(err)
	}
	c1.Close()

	c2 := pair(t)
	w, err := Wait(testCtx(t), c2, j.ID)
	if err != nil || w.ExitCode != 0 {
		t.Fatalf("wait: %+v %v", w, err)
	}
	r, _ := Logs(testCtx(t), c2, LogsRequest{Job: j.ID})
	if got := readLogs(t, r); got != "survived\n" {
		t.Fatalf("log %q", got)
	}
}

// A restarted host (a fresh job manager over the same directory) still
// lists, checks and signals jobs it did not spawn, and marks jobs that ended
// unobserved as unknown.
func TestJobMetadataReload(t *testing.T) {
	dir := useJobsDir(t)
	conn := pair(t)
	ctx := testCtx(t)
	j, err := Spawn(ctx, conn, SpawnRequest{Command: "sleep 1000", Name: "sleeper"})
	if err != nil {
		t.Fatal(err)
	}

	restarted := newJobManager(jobs.dir)
	list, err := restarted.list()
	if err != nil || len(list) != 1 || list[0].State != JobRunning || list[0].Name != "sleeper" {
		t.Fatalf("after restart: %+v %v", list, err)
	}
	if _, err := restarted.kill("sleeper", "TERM"); err != nil {
		t.Fatal(err)
	}
	got, err := restarted.wait(ctx, j.ID)
	if err != nil {
		t.Fatal(err)
	}
	// The original manager reaped it and recorded the status on disk.
	waitFor(t, "exit to be recorded", 5*time.Second, func() bool {
		got, _ = restarted.wait(ctx, j.ID)
		return got.State == JobExited
	})
	if got.ExitCode != 143 {
		t.Fatalf("restarted view: %+v", got)
	}

	// A job whose process vanished while no host was watching.
	gone := exec.Command("true")
	gone.Run()
	meta := jobMeta{Job: Job{ID: "deadbeef", Command: "lost", PID: gone.Process.Pid, State: JobRunning,
		Started: time.Now(), Log: filepath.Join(dir, "deadbeef.log")}}
	b, _ := json.Marshal(meta)
	os.WriteFile(filepath.Join(dir, "deadbeef.json"), b, 0o600)
	os.WriteFile(meta.Log, []byte("partial\n"), 0o600)
	list, _ = restarted.list()
	var lost *Job
	for i := range list {
		if list[i].ID == "deadbeef" {
			lost = &list[i]
		}
	}
	if lost == nil || lost.State != JobUnknown {
		t.Fatalf("lost job: %+v", lost)
	}
	r, err := Logs(ctx, conn, LogsRequest{Job: "deadbeef", Follow: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := readLogs(t, r); got != "partial\n" || r.Job().State != JobUnknown {
		t.Fatalf("follow of a lost job: %q %+v", got, r.Job())
	}
}

func TestRemoveJobs(t *testing.T) {
	dir := useJobsDir(t)
	conn := pair(t)
	ctx := testCtx(t)
	done, _ := Spawn(ctx, conn, SpawnRequest{Command: "true", Name: "dup"})
	Wait(ctx, conn, done.ID)
	running, _ := Spawn(ctx, conn, SpawnRequest{Command: "sleep 1000", Name: "dup"})
	t.Cleanup(func() {
		// Let the host record the exit before the directory is deleted.
		killGroup(running.PID)
		jobs.wait(context.Background(), running.ID)
	})

	if _, err := Logs(ctx, conn, LogsRequest{Job: "dup"}); err != nil {
		t.Fatal(err) // opening succeeds; the error arrives on Read
	}
	if _, err := Wait(ctx, conn, "dup"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous name: %v", err)
	}
	if _, err := RemoveJobs(ctx, conn, RemoveRequest{Jobs: []string{running.ID}}); err == nil {
		t.Fatal("removed a running job")
	}
	ids, err := RemoveJobs(ctx, conn, RemoveRequest{Exited: true})
	if err != nil || len(ids) != 1 || ids[0] != done.ID {
		t.Fatalf("remove --exited: %v %v", ids, err)
	}
	if _, err := os.Stat(filepath.Join(dir, done.ID+".log")); !os.IsNotExist(err) {
		t.Fatalf("log not removed: %v", err)
	}
	list, _ := Jobs(ctx, conn)
	if len(list) != 1 || list[0].ID != running.ID {
		t.Fatalf("remaining jobs %+v", list)
	}
	if _, err := Wait(ctx, conn, "../etc"); err == nil {
		t.Fatal("path-like job reference accepted")
	}
}

// If the metadata file cannot be updated when a job ends, the host still
// knows how its own child ended: the job must not turn "unknown".
func TestJobExitSurvivesUnwritableMetadata(t *testing.T) {
	dir := useJobsDir(t)
	conn := pair(t)
	ctx := testCtx(t)
	j, err := Spawn(ctx, conn, SpawnRequest{Command: "sleep 0.3; exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	// A non-empty directory in the file's place makes both reading and
	// replacing it fail.
	meta := filepath.Join(dir, j.ID+".json")
	if err := os.Remove(meta); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(meta, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	w, err := Wait(ctx, conn, j.ID)
	if err != nil || w.State != JobExited || w.ExitCode != 3 || w.Ended == nil {
		t.Fatalf("wait: %+v %v", w, err)
	}
	list, err := Jobs(ctx, conn)
	if err != nil || len(list) != 1 || list[0].State != JobExited || list[0].ExitCode != 3 {
		t.Fatalf("jobs: %+v %v", list, err)
	}
}
