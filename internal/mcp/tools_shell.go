package mcp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/svc/shell"
)

// Limits for the shell tools. They keep one call from filling the model's
// context or holding the protocol connection indefinitely.
const (
	defaultExecTimeout = 60 // seconds
	maxExecTimeout     = 900
	defaultOutputBytes = 32 << 10
	maxOutputBytes     = 1 << 20
	maxStdinBytes      = 1 << 20
	defaultLogLines    = 100
	maxLogLines        = 10000
	maxLogWait         = 120 // seconds
	defaultJobWait     = 30
	maxJobWait         = 300
)

type execIn struct {
	Target         string            `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	Command        string            `json:"command" jsonschema:"Command line in the REMOTE machine's shell language: POSIX sh syntax on Linux and macOS (run by $SHELL -c), PowerShell syntax on Windows. Check remohop_info (os) if unsure."`
	Cwd            string            `json:"cwd,omitempty" jsonschema:"Working directory on the remote machine. Empty means the remote user's home directory."`
	Env            map[string]string `json:"env,omitempty" jsonschema:"Extra environment variables for this command."`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty" jsonschema:"Kill the command after this many seconds (default 60, max 900). For longer work use remohop_spawn."`
	Stdin          string            `json:"stdin,omitempty" jsonschema:"Text fed to the command's standard input (max 1 MiB)."`
	MaxOutputBytes int               `json:"max_output_bytes,omitempty" jsonschema:"Keep at most this many bytes of stdout and of stderr each (default 32768, max 1048576). The beginning is kept; the rest is counted and dropped."`
}

type execOut struct {
	Stdout          string `json:"stdout"`
	Stderr          string `json:"stderr"`
	ExitCode        int    `json:"exit_code"`
	Signal          string `json:"signal,omitempty"`
	TimedOut        bool   `json:"timed_out"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
	StdoutBytes     int64  `json:"stdout_bytes"`
	StderrBytes     int64  `json:"stderr_bytes"`
	DurationMS      int64  `json:"duration_ms"`
}

type spawnIn struct {
	Target  string            `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	Command string            `json:"command" jsonschema:"Command line to run in the background, in the REMOTE machine's shell language: POSIX sh on Linux and macOS, PowerShell on Windows."`
	Name    string            `json:"name,omitempty" jsonschema:"Optional label; other job tools accept it in place of the job id if it is unique."`
	Cwd     string            `json:"cwd,omitempty" jsonschema:"Working directory on the remote machine. Empty means the remote user's home directory."`
	Env     map[string]string `json:"env,omitempty" jsonschema:"Extra environment variables for the job."`
}

type jobInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name,omitempty"`
	Command  string `json:"command"`
	Cwd      string `json:"cwd"`
	PID      int    `json:"pid"`
	State    string `json:"state" jsonschema:"running, exited, or unknown (ended while the host daemon was not watching)."`
	ExitCode *int   `json:"exit_code,omitempty"`
	Signal   string `json:"signal,omitempty"`
	Started  string `json:"started"`
	Ended    string `json:"ended,omitempty"`
	LogPath  string `json:"log_path" jsonschema:"Path of the combined stdout+stderr log on the remote machine."`
	LogBytes int64  `json:"log_bytes"`
}

func toJobInfo(j shell.Job) jobInfo {
	ji := jobInfo{
		ID: j.ID, Name: j.Name, Command: j.Command, Cwd: j.Cwd, PID: j.PID, State: j.State,
		Signal: j.SignalName, Started: j.Started.Format(time.RFC3339), LogPath: j.Log, LogBytes: j.LogSize,
	}
	if j.State == shell.JobExited {
		ji.ExitCode = &j.ExitCode
	}
	if j.Ended != nil {
		ji.Ended = j.Ended.Format(time.RFC3339)
	}
	cleanStrings(&ji)
	return ji
}

func (j jobInfo) line() string {
	s := fmt.Sprintf("%s  %s", j.ID, j.State)
	if j.ExitCode != nil {
		s += fmt.Sprintf(" (exit %d)", *j.ExitCode)
	}
	if j.Signal != "" {
		s += " signal " + j.Signal
	}
	if j.Name != "" {
		s += "  [" + j.Name + "]"
	}
	return s + "  " + truncUTF8(j.Command, 120)
}

type jobsIn struct {
	Target      string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	RunningOnly bool   `json:"running_only,omitempty" jsonschema:"Only list jobs that are still running."`
}

type jobsOut struct {
	Jobs []jobInfo `json:"jobs"`
}

type jobLogsIn struct {
	Target      string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	Job         string `json:"job" jsonschema:"Job id (from remohop_spawn or remohop_jobs) or its unique name."`
	TailLines   int    `json:"tail_lines,omitempty" jsonschema:"Return the last N lines of the log (default 100, max 10000)."`
	MaxBytes    int    `json:"max_bytes,omitempty" jsonschema:"Cap the returned text at this many bytes, keeping the newest (default 32768, max 1048576)."`
	WaitSeconds int    `json:"wait_seconds,omitempty" jsonschema:"If > 0, keep following the log for up to this many seconds (max 120) or until the job ends, and include the new output. Never waits longer."`
}

type jobLogsOut struct {
	Output    string  `json:"output"`
	Truncated bool    `json:"truncated" jsonschema:"True if older output was dropped to fit max_bytes."`
	Job       jobInfo `json:"job"`
}

type jobKillIn struct {
	Target string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	Job    string `json:"job" jsonschema:"Job id or unique name."`
	Signal string `json:"signal,omitempty" jsonschema:"Signal name or number sent to the job's whole process group (default TERM; use KILL to force)."`
}

type jobWaitIn struct {
	Target         string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
	Job            string `json:"job" jsonschema:"Job id or unique name."`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"Give up waiting after this many seconds (default 30, max 300); the job keeps running."`
}

type jobWaitOut struct {
	TimedOut bool    `json:"timed_out" jsonschema:"True if the job was still running when the wait gave up."`
	Job      jobInfo `json:"job"`
}

func envList(m map[string]string) ([]string, error) {
	out := make([]string, 0, len(m))
	for k, v := range m {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return nil, fmt.Errorf("invalid environment variable %q", k)
		}
		out = append(out, k+"="+v)
	}
	slices.Sort(out)
	return out, nil
}

func (s *Server) registerShell() {
	addTool(s, "remohop_exec", "Run a command on a remote machine",
		"Run one shell command on the remote machine and wait for it to finish. Returns stdout, stderr, the exit code, "+
			"whether it timed out, and whether output was truncated. A non-zero exit code is a normal result, not a tool error. "+
			"The command is interpreted by the remote host's shell: POSIX sh on Linux and macOS, PowerShell on Windows (there: Get-ChildItem, $env:NAME, `;` between statements, no `&&` in Windows PowerShell 5, stdin via $input). remohop_info reports the OS; call it first if you do not know. "+
			"The command runs non-interactively with the remote user's full privileges and no terminal (do not run editors, pagers or anything that prompts). "+
			"For commands that take more than about a minute, or servers and watchers that never exit, use remohop_spawn instead. "+
			"Keep output small: filter on the remote side (grep, tail -n, head, wc) rather than printing whole logs. "+
			"If the session drops mid-call the command may or may not have run, so this tool is not retried automatically.",
		destructive(false), s.exec)
	addTool(s, "remohop_spawn", "Start a background job on a remote machine",
		"Start a command as a detached background job on the remote machine and return its job id immediately. "+
			"Write the command for the remote host's shell: POSIX sh on Linux and macOS, PowerShell on Windows (remohop_info reports the OS). "+
			"Output goes to a log file; the job keeps running after this call and even if the connection drops. "+
			"Use remohop_job_logs to read output, remohop_job_wait to wait for completion and remohop_job_kill to stop it. "+
			"Use this for builds, test suites, servers, long downloads and anything over a minute.",
		destructive(false), s.spawn)
	addTool(s, "remohop_jobs", "List background jobs on a remote machine",
		"List background jobs started with remohop_spawn (or `remohop <ID> spawn`) on the remote machine, with state and exit code.",
		readOnly(), s.jobs)
	addTool(s, "remohop_job_logs", "Read a background job's output",
		"Read the combined stdout+stderr log of a background job: the last tail_lines lines, capped at max_bytes. "+
			"Set wait_seconds to follow the log for a short while (returns early when the job ends) instead of polling. "+
			"The result also tells you whether the job is still running and its exit code.",
		readOnly(), s.jobLogs)
	addTool(s, "remohop_job_kill", "Stop a background job",
		"Send a signal (default TERM) to a background job's whole process group on the remote machine. "+
			"Use signal KILL if TERM does not stop it. Returns the job's state after signalling.",
		destructive(true), s.jobKill)
	addTool(s, "remohop_job_wait", "Wait for a background job to finish",
		"Block until a background job ends or timeout_seconds passes (default 30, max 300), then report its state and exit code. "+
			"If it times out the job is still running; call again or read remohop_job_logs.",
		readOnly(), s.jobWait)
}

func (s *Server) exec(ctx context.Context, in execIn) (*reply[execOut], error) {
	if strings.TrimSpace(in.Command) == "" {
		return nil, errors.New("command is empty")
	}
	if len(in.Stdin) > maxStdinBytes {
		return nil, fmt.Errorf("stdin is %d bytes; the limit is %d (write a file with remohop_write_file and read it in the command instead)", len(in.Stdin), maxStdinBytes)
	}
	env, err := envList(in.Env)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(clamp(in.TimeoutSeconds, defaultExecTimeout, maxExecTimeout)) * time.Second
	limit := clamp(in.MaxOutputBytes, defaultOutputBytes, maxOutputBytes)
	req := shell.ExecRequest{Command: in.Command, Cwd: in.Cwd, Env: env, Timeout: timeout}
	if in.Stdin != "" {
		req.Stdin = strings.NewReader(in.Stdin)
	}

	var res *shell.CaptureResult
	// The host enforces the timeout; this guard only covers a host that
	// stops answering altogether.
	cctx, cancel := context.WithTimeout(ctx, timeout+30*time.Second)
	defer cancel()
	err = s.withConn(cctx, in.Target, false, func(ctx context.Context, c *peer.Conn) (err error) {
		if req.Stdin != nil {
			req.Stdin = strings.NewReader(in.Stdin)
		}
		res, err = shell.Capture(ctx, c, req, limit)
		return err
	})
	if err != nil {
		var se *shell.StartError
		if errors.As(err, &se) {
			return nil, fmt.Errorf("the remote host could not start the command: %s (check cwd and that the shell exists)", se.Msg)
		}
		return nil, err
	}
	out := execOut{
		Stdout: clean(string(res.Stdout)), Stderr: clean(string(res.Stderr)),
		ExitCode: res.ExitCode, Signal: res.SignalName, TimedOut: res.TimedOut,
		StdoutTruncated: res.StdoutTruncated, StderrTruncated: res.StderrTruncated,
		StdoutBytes: res.StdoutBytes, StderrBytes: res.StderrBytes, DurationMS: res.Duration.Milliseconds(),
	}
	return textReply(out, out.render(timeout)), nil
}

func (o execOut) render(timeout time.Duration) string {
	var b strings.Builder
	switch {
	case o.TimedOut:
		fmt.Fprintf(&b, "TIMED OUT after %s and was killed (exit code %d)", timeout, o.ExitCode)
	case o.Signal != "":
		fmt.Fprintf(&b, "killed by signal %s (exit code %d)", o.Signal, o.ExitCode)
	default:
		fmt.Fprintf(&b, "exit code %d", o.ExitCode)
	}
	fmt.Fprintf(&b, " in %s\n", (time.Duration(o.DurationMS) * time.Millisecond).Round(time.Millisecond))
	section := func(name, text string, truncated bool, total int64) {
		if text == "" && !truncated {
			return
		}
		fmt.Fprintf(&b, "--- %s ---\n%s", name, text)
		if !strings.HasSuffix(text, "\n") {
			b.WriteByte('\n')
		}
		if truncated {
			fmt.Fprintf(&b, "[%s truncated: kept the first %d of %d bytes; narrow the output with grep/head/tail or write it to a file]\n", name, len(text), total)
		}
	}
	section("stdout", o.Stdout, o.StdoutTruncated, o.StdoutBytes)
	section("stderr", o.Stderr, o.StderrTruncated, o.StderrBytes)
	if o.Stdout == "" && o.Stderr == "" {
		b.WriteString("(no output)\n")
	}
	return b.String()
}

func (s *Server) spawn(ctx context.Context, in spawnIn) (*reply[jobInfo], error) {
	if strings.TrimSpace(in.Command) == "" {
		return nil, errors.New("command is empty")
	}
	env, err := envList(in.Env)
	if err != nil {
		return nil, err
	}
	var job *shell.Job
	err = s.withConn(ctx, in.Target, false, func(ctx context.Context, c *peer.Conn) (err error) {
		job, err = shell.Spawn(ctx, c, shell.SpawnRequest{Command: in.Command, Name: in.Name, Cwd: in.Cwd, Env: env})
		return err
	})
	if err != nil {
		return nil, err
	}
	ji := toJobInfo(*job)
	return textReply(ji, fmt.Sprintf("started job %s (pid %d), log %s\nRead output with remohop_job_logs, wait with remohop_job_wait, stop with remohop_job_kill.", ji.ID, ji.PID, ji.LogPath)), nil
}

func (s *Server) jobs(ctx context.Context, in jobsIn) (*reply[jobsOut], error) {
	var list []shell.Job
	err := s.withConn(ctx, in.Target, true, func(ctx context.Context, c *peer.Conn) (err error) {
		list, err = shell.Jobs(ctx, c)
		return err
	})
	if err != nil {
		return nil, err
	}
	out := jobsOut{Jobs: []jobInfo{}}
	var b strings.Builder
	for _, j := range list {
		if in.RunningOnly && j.State != shell.JobRunning {
			continue
		}
		ji := toJobInfo(j)
		out.Jobs = append(out.Jobs, ji)
		b.WriteString(ji.line() + "\n")
	}
	if len(out.Jobs) == 0 {
		b.WriteString("no jobs\n")
	}
	return textReply(out, b.String()), nil
}

// tailBuffer keeps the newest max bytes written to it.
type tailBuffer struct {
	max     int
	buf     []byte
	dropped bool
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if over := len(t.buf) - t.max; over > 0 {
		t.buf = append(t.buf[:0], t.buf[over:]...)
		t.dropped = true
	}
	return len(p), nil
}

func (s *Server) jobLogs(ctx context.Context, in jobLogsIn) (*reply[jobLogsOut], error) {
	if in.Job == "" {
		return nil, errors.New("job is required")
	}
	lines := clamp(in.TailLines, defaultLogLines, maxLogLines)
	tail := &tailBuffer{max: clamp(in.MaxBytes, defaultOutputBytes, maxOutputBytes)}
	wait := min(max(in.WaitSeconds, 0), maxLogWait)

	var job *shell.Job
	err := s.withConn(ctx, in.Target, true, func(ctx context.Context, c *peer.Conn) error {
		tail.buf, tail.dropped = tail.buf[:0], false
		rctx := ctx
		if wait > 0 {
			var cancel context.CancelFunc
			rctx, cancel = context.WithTimeout(ctx, time.Duration(wait)*time.Second)
			defer cancel()
		}
		lr, err := shell.Logs(rctx, c, shell.LogsRequest{Job: in.Job, Follow: wait > 0, Tail: lines})
		if err != nil {
			return err
		}
		defer lr.Close()
		_, err = io.Copy(tail, lr)
		switch {
		case err == nil:
			job = lr.Job()
			return nil
		case ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded):
			// The follow window elapsed; the job is still running.
			return nil
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if job == nil {
		if job, err = s.lookupJob(ctx, in.Target, in.Job); err != nil {
			return nil, err
		}
	}
	out := jobLogsOut{Output: clean(string(tail.buf)), Truncated: tail.dropped, Job: toJobInfo(*job)}
	var b strings.Builder
	fmt.Fprintf(&b, "job %s: %s", out.Job.ID, out.Job.State)
	if out.Job.ExitCode != nil {
		fmt.Fprintf(&b, " (exit code %d)", *out.Job.ExitCode)
	}
	b.WriteString("\n")
	if out.Truncated {
		fmt.Fprintf(&b, "[older output dropped to fit %d bytes]\n", tail.max)
	}
	b.WriteString("--- log ---\n" + out.Output)
	if out.Output != "" && !strings.HasSuffix(out.Output, "\n") {
		b.WriteByte('\n')
	}
	return textReply(out, b.String()), nil
}

// lookupJob finds a job by id or name in the host's job list.
func (s *Server) lookupJob(ctx context.Context, target, ref string) (*shell.Job, error) {
	var found *shell.Job
	err := s.withConn(ctx, target, true, func(ctx context.Context, c *peer.Conn) error {
		list, err := shell.Jobs(ctx, c)
		if err != nil {
			return err
		}
		for i := range list {
			if list[i].ID == ref || list[i].Name == ref {
				found = &list[i]
				return nil
			}
		}
		return fmt.Errorf("no such job %q (see remohop_jobs)", ref)
	})
	return found, err
}

func (s *Server) jobKill(ctx context.Context, in jobKillIn) (*reply[jobInfo], error) {
	if in.Job == "" {
		return nil, errors.New("job is required")
	}
	var job *shell.Job
	err := s.withConn(ctx, in.Target, true, func(ctx context.Context, c *peer.Conn) (err error) {
		job, err = shell.Kill(ctx, c, in.Job, in.Signal)
		return err
	})
	if err != nil {
		return nil, err
	}
	if job == nil {
		return nil, fmt.Errorf("the host returned no state for job %q", in.Job)
	}
	ji := toJobInfo(*job)
	return textReply(ji, "signal sent. "+ji.line()), nil
}

func (s *Server) jobWait(ctx context.Context, in jobWaitIn) (*reply[jobWaitOut], error) {
	if in.Job == "" {
		return nil, errors.New("job is required")
	}
	d := time.Duration(clamp(in.TimeoutSeconds, defaultJobWait, maxJobWait)) * time.Second
	var job *shell.Job
	timedOut := false
	err := s.withConn(ctx, in.Target, true, func(ctx context.Context, c *peer.Conn) (err error) {
		wctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		job, err = shell.Wait(wctx, c, in.Job)
		if err != nil && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			job, timedOut, err = nil, true, nil
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	if timedOut {
		if job, err = s.lookupJob(ctx, in.Target, in.Job); err != nil {
			return nil, err
		}
	}
	out := jobWaitOut{TimedOut: timedOut, Job: toJobInfo(*job)}
	msg := out.Job.line()
	if timedOut {
		msg = fmt.Sprintf("still running after %s; %s", d, msg)
	}
	return textReply(out, msg), nil
}
