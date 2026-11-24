//go:build !windows

package shell

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCLIExec(t *testing.T) {
	conn := pair(t)
	env, out, errb := cliEnv(conn, strings.NewReader("piped-in"))
	code := runCLI(t, env, "shell", "-c", "cat; echo; echo E >&2; exit 5", "--env", "A=1", "--cwd", "/")
	if code != 5 || out.String() != "piped-in\n" || errb.String() != "E\n" {
		t.Fatalf("exit %d stdout %q stderr %q", code, out.String(), errb.String())
	}

	env, out, _ = cliEnv(conn, strings.NewReader("ignored"))
	if code := runCLI(t, env, "shell", "-n", "-c", "wc -c"); code != 0 || strings.TrimSpace(out.String()) != "0" {
		t.Fatalf("-n: exit %d out %q", code, out.String())
	}

	env, _, _ = cliEnv(conn, nil)
	if code := runCLI(t, env, "shell", "-c", "kill -KILL $$"); code != 137 {
		t.Fatalf("signal exit %d", code)
	}

	env, _, errb = cliEnv(conn, nil)
	if code := runCLI(t, env, "shell", "-c", "sleep 10", "--timeout", "200ms"); code != exitTimeout ||
		!strings.Contains(errb.String(), "timed out") {
		t.Fatalf("timeout exit %d stderr %q", code, errb.String())
	}

	env, _, errb = cliEnv(conn, nil)
	if code := runCLI(t, env, "shell", "-c", "true", "--cwd", "/no/such/dir"); code != 255 || !strings.Contains(errb.String(), "remote:") {
		t.Fatalf("start error exit %d stderr %q", code, errb.String())
	}
}

func TestCLIShellUsageErrors(t *testing.T) {
	conn := pair(t)
	for _, args := range [][]string{
		{"shell", "-s", "x", "-c", "ls"},
		{"shell", "--new", "-c", "ls"},
		{"shell", "--timeout", "1s"},
		{"shell", "-c", "ls", "extra"},
		{"shell", "uname"},
		{"shell", "-e", "xx"},
		{"shell", "--bogus"},
		{"shell", "kill"},
		{"logs"},
		{"kill"},
		{"spawn"},
		{"spawn", "--name", "bad name", "true"},
		{"jobs", "rm"},
	} {
		env, _, _ := cliEnv(conn, nil)
		if code := runCLI(t, env, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	env, _, _ := cliEnv(conn, nil)
	if code := runCLI(t, env, "shell", "-h"); code != 0 {
		t.Errorf("-h exit %d", code)
	}
}

func TestCLIShellList(t *testing.T) {
	conn := pair(t)
	ctx := testCtx(t)
	started := make(chan *AttachResult, 1)
	go Attach(ctx, conn, AttachRequest{Name: "listed", Rows: 30, Cols: 90}, AttachIO{OnAttach: func(r *AttachResult) { started <- r }})
	<-started
	t.Cleanup(func() { KillSession(ctx, conn, "listed") })

	env, out, _ := cliEnv(conn, nil)
	if code := runCLI(t, env, "shell", "ls", "--json"); code != 0 {
		t.Fatalf("exit %d", code)
	}
	var list []SessionInfo
	if err := json.Unmarshal([]byte(out.String()), &list); err != nil {
		t.Fatalf("bad json %q: %v", out.String(), err)
	}
	var found bool
	for _, s := range list {
		if s.Name == "listed" {
			found = s.Attached && s.Rows == 30 && s.Cols == 90 && s.PID > 0
		}
	}
	if !found {
		t.Fatalf("session not listed correctly: %+v", list)
	}
}

func TestCLIJobs(t *testing.T) {
	useJobsDir(t)
	conn := pair(t)

	env, out, errb := cliEnv(conn, nil)
	if code := runCLI(t, env, "spawn", "--name", "web", "echo", "serving;", "sleep", "1000"); code != 0 {
		t.Fatalf("spawn exit %d: %s", code, errb.String())
	}
	id := strings.TrimSpace(out.String())
	if len(id) != 8 || !strings.Contains(errb.String(), "logs -f "+id) {
		t.Fatalf("spawn printed %q / %q", out.String(), errb.String())
	}

	env, out, _ = cliEnv(conn, nil)
	if code := runCLI(t, env, "spawn", "--json", "--env", "X=7", "echo $X; exit 3"); code != 0 {
		t.Fatalf("spawn --json exit %d", code)
	}
	var j Job
	if err := json.Unmarshal([]byte(out.String()), &j); err != nil || j.Command != "echo $X; exit 3" {
		t.Fatalf("spawn --json: %q %v", out.String(), err)
	}

	// logs -f exits with the job's exit code.
	env, out, _ = cliEnv(conn, nil)
	if code := runCLI(t, env, "logs", j.ID, "-f"); code != 3 || out.String() != "7\n" {
		t.Fatalf("logs -f: exit %d out %q", code, out.String())
	}
	env, _, _ = cliEnv(conn, nil)
	if code := runCLI(t, env, "jobs", "wait", j.ID); code != 3 {
		t.Fatalf("jobs wait: exit %d", code)
	}

	env, out, _ = cliEnv(conn, nil)
	if code := runCLI(t, env, "jobs"); code != 0 || !strings.Contains(out.String(), "running") ||
		!strings.Contains(out.String(), "exited(3)") || !strings.Contains(out.String(), "web") {
		t.Fatalf("jobs: exit %d\n%s", code, out.String())
	}
	env, out, _ = cliEnv(conn, nil)
	runCLI(t, env, "jobs", "--json")
	var list []Job
	if err := json.Unmarshal([]byte(out.String()), &list); err != nil || len(list) != 2 {
		t.Fatalf("jobs --json: %q %v", out.String(), err)
	}

	env, out, _ = cliEnv(conn, nil)
	waitFor(t, "web job output", 5*time.Second, func() bool {
		env, out, _ = cliEnv(conn, nil)
		runCLI(t, env, "logs", "--tail", "1", "web")
		return out.String() == "serving\n"
	})

	env, out, _ = cliEnv(conn, nil)
	if code := runCLI(t, env, "kill", "-s", "INT", "web"); code != 0 || !strings.Contains(out.String(), "sent INT to job "+id) {
		t.Fatalf("kill: exit %d out %q", code, out.String())
	}
	env, _, _ = cliEnv(conn, nil)
	if code := runCLI(t, env, "jobs", "wait", "web"); code != 130 {
		t.Fatalf("wait after SIGINT: exit %d", code)
	}

	env, out, _ = cliEnv(conn, nil)
	if code := runCLI(t, env, "jobs", "rm", "--exited"); code != 0 || strings.Count(out.String(), "removed") != 2 {
		t.Fatalf("jobs rm: exit %d out %q", code, out.String())
	}
	env, out, _ = cliEnv(conn, nil)
	if runCLI(t, env, "jobs"); out.String() != "no jobs\n" {
		t.Fatalf("jobs after rm: %q", out.String())
	}
	env, _, errb = cliEnv(conn, nil)
	if code := runCLI(t, env, "logs", "nosuchjob"); code != 1 || !strings.Contains(errb.String(), "no job") {
		t.Fatalf("logs of missing job: exit %d %q", code, errb.String())
	}
}
