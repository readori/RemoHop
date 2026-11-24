package shell

import (
	"testing"
	"time"
)

func TestWinSpawnLogsWait(t *testing.T) {
	useJobsDir(t)
	conn := pair(t)
	ctx := longCtx(t)
	j, err := Spawn(ctx, conn, SpawnRequest{
		Command: "Write-Output start; Start-Sleep -Milliseconds 500; Write-Output ([string][char]0x00e9 + 'nd'); exit 4",
		Name:    "wjob",
	})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Logs(ctx, conn, LogsRequest{Job: "wjob", Follow: true})
	if err != nil {
		t.Fatal(err)
	}
	got := crlf(readLogs(t, r))
	r.Close()
	if got != "start\nénd\n" {
		t.Fatalf("log %q", got)
	}
	if fj := r.Job(); fj == nil || fj.State != JobExited || fj.ExitCode != 4 {
		t.Fatalf("job at end of log %+v", r.Job())
	}
	if w, err := Wait(ctx, conn, j.ID); err != nil || w.ExitCode != 4 {
		t.Fatalf("wait %+v %v", w, err)
	}
}

func TestWinKillJob(t *testing.T) {
	useJobsDir(t)
	conn := pair(t)
	ctx := longCtx(t)
	j, err := Spawn(ctx, conn, SpawnRequest{Command: "Start-Sleep 600"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "job to be running", 30*time.Second, func() bool { return procAlive(j.PID, 0) })
	if _, err := Kill(ctx, conn, j.ID, "KILL"); err != nil {
		t.Fatal(err)
	}
	w, err := Wait(ctx, conn, j.ID)
	// A killed Windows process has no signal in its exit status: the job
	// reports exit code 1 plus the signal the client asked for.
	if err != nil || w.State != JobExited || w.ExitCode != 1 || w.SignalName != "KILL" || w.Signal != 9 || w.Ended == nil {
		t.Fatalf("after kill %+v %v", w, err)
	}
	list, err := Jobs(ctx, conn)
	if err != nil || len(list) != 1 || list[0].ExitCode != 1 || list[0].SignalName != "KILL" {
		t.Fatalf("listed after kill %+v %v", list, err)
	}
	if _, err := Kill(ctx, conn, j.ID, "USR1"); err == nil {
		t.Fatal("unsupported signal accepted")
	}
}

func TestWinJobSurvivesSessionClose(t *testing.T) {
	useJobsDir(t)
	c1, _ := peertestPair(t)
	j, err := Spawn(longCtx(t), c1, SpawnRequest{Command: "Start-Sleep -Milliseconds 1500; Write-Output survived"})
	if err != nil {
		t.Fatal(err)
	}
	c1.Close()
	c2 := pair(t)
	w, err := Wait(longCtx(t), c2, j.ID)
	if err != nil || w.ExitCode != 0 {
		t.Fatalf("wait %+v %v", w, err)
	}
	r, _ := Logs(longCtx(t), c2, LogsRequest{Job: j.ID})
	if got := crlf(readLogs(t, r)); got != "survived\n" {
		t.Fatalf("log %q", got)
	}
}

// A job that ends on its own keeps its real exit code and no signal.
func TestWinJobNaturalExitHasNoSignal(t *testing.T) {
	useJobsDir(t)
	conn := pair(t)
	ctx := longCtx(t)
	j, err := Spawn(ctx, conn, SpawnRequest{Command: "exit 6"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := Wait(ctx, conn, j.ID)
	if err != nil || w.State != JobExited || w.ExitCode != 6 || w.SignalName != "" || w.Signal != 0 {
		t.Fatalf("after exit %+v %v", w, err)
	}
}
