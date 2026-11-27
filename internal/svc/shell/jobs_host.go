package shell

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/readori/RemoHop/internal/config"
	"github.com/readori/RemoHop/internal/grant"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

// Job states.
const (
	JobRunning = "running"
	JobExited  = "exited"
	// JobUnknown is a job that is no longer running but whose exit status
	// was never recorded, because the host process that spawned it was not
	// around to reap it (remohop host restarted while it ran).
	JobUnknown = "unknown"
)

// Job is a background job on the host. Jobs run in their own session with
// stdin from /dev/null and stdout+stderr appended to Log; they outlive the
// client session that spawned them and the host process itself. Logs are
// not rotated: they grow until the job is removed.
type Job struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Command string `json:"command"`
	Cwd     string `json:"cwd"`
	PID     int    `json:"pid"`
	State   string `json:"state"`
	// ExitCode is meaningful in state "exited": the exit status, or 128+n
	// for death by signal n. Windows has no signals: a job killed there
	// reports exit code 1, with Signal and SignalName naming what Kill sent.
	ExitCode   int        `json:"exit_code"`
	Signal     int        `json:"signal,omitempty"`
	SignalName string     `json:"signal_name,omitempty"`
	Started    time.Time  `json:"started"`
	Ended      *time.Time `json:"ended,omitempty"`
	// Log is the log file's path on the host, LogSize its current size.
	Log     string `json:"log"`
	LogSize int64  `json:"log_size"`
	// Grant is the grant the job was spawned under, empty for full
	// access. Owner is who spawned it: the grant and the client's
	// identity (grant.OwnerOf). A session under a grant sees and handles
	// only the jobs of its own owner, so two identities on one grant an
	// authorizer gives do not share them.
	Grant string `json:"grant,omitempty"`
	Owner string `json:"owner,omitempty"`
}

// jobMeta is what is persisted per job (<id>.json next to <id>.log).
type jobMeta struct {
	Job
	// Ident tells the job's process apart from a later process reusing
	// its PID (the process start time where the OS exposes it).
	Ident uint64 `json:"ident,omitempty"`
	// KillSignal is the last signal a client sent the job with Kill. Where
	// the OS cannot say how a process died (Windows: a killed process just
	// exits with code 1), it is what the recorded exit reports.
	KillSignal string `json:"kill_signal,omitempty"`
}

type spawnReq struct {
	Command string   `json:"command"`
	Name    string   `json:"name,omitempty"`
	Cwd     string   `json:"cwd,omitempty"`
	Env     []string `json:"env,omitempty"`
}

type jobsReq struct {
	Op     string   `json:"op"` // list, kill, wait, remove, logs
	Job    string   `json:"job,omitempty"`
	Signal string   `json:"signal,omitempty"`
	Follow bool     `json:"follow,omitempty"`
	Tail   int      `json:"tail,omitempty"`
	Jobs   []string `json:"jobs,omitempty"`
	Exited bool     `json:"exited,omitempty"`
}

type jobsReply struct {
	Error   string   `json:"error,omitempty"`
	Job     *Job     `json:"job,omitempty"`
	Jobs    []Job    `json:"jobs,omitempty"`
	Removed []string `json:"removed,omitempty"`
}

// jobPoll is how often the host re-checks a job it cannot wait for (one a
// previous host process spawned) and a followed log file.
const jobPoll = 250 * time.Millisecond

func init() {
	peer.Handle(svcSpawn, handleSpawn)
	peer.Handle(svcJobs, handleJobs)
}

// jobManager tracks the jobs this host process spawned. Everything else
// about a job lives on disk, so a restarted host (a fresh jobManager) still
// lists them.
type jobManager struct {
	dir func() (string, error)

	// mu serialises metadata file access and guards children.
	mu sync.Mutex
	// children maps the ID of each job this process spawned and has not
	// reaped yet to a channel closed once it has been reaped and its exit
	// recorded.
	children map[string]chan struct{}
	// mem holds what this process knows first-hand about jobs it started:
	// the metadata of each running child, and the final state of reaped
	// ones whose metadata file could not be updated at that moment. It
	// backs the files up, so a job this host started is never lost or
	// reported as "unknown" while the host runs.
	mem map[string]*jobMeta
	// kills remembers the signal last sent to each of our running children.
	kills map[string]string
}

var jobs = newJobManager(func() (string, error) { return config.SubDir("jobs") })

func newJobManager(dir func() (string, error)) *jobManager {
	return &jobManager{dir: dir, children: map[string]chan struct{}{}, mem: map[string]*jobMeta{}, kills: map[string]string{}}
}

func handleSpawn(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
	var req spawnReq
	if err := peer.DecodeReq(raw, &req); err != nil {
		return wire.WriteJSON(s, jobsReply{Error: "bad request: " + err.Error()})
	}
	name, owner := "", ""
	if g := grant.Of(c); g != nil {
		if err := g.WorkDir(ctx, "spawn", resolveDir(req.Cwd)); err != nil {
			return wire.WriteJSON(s, jobsReply{Error: err.Error()})
		}
		if err := g.Command(ctx, "spawn", req.Command, false, req.Env); err != nil {
			return wire.WriteJSON(s, jobsReply{Error: err.Error()})
		}
		name, owner = g.Name(), grant.OwnerOf(g)
	}
	j, err := jobs.spawn(&req, name, owner)
	if err != nil {
		return wire.WriteJSON(s, jobsReply{Error: err.Error()})
	}
	return wire.WriteJSON(s, jobsReply{Job: j})
}

func handleJobs(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
	var req jobsReq
	if err := peer.DecodeReq(raw, &req); err != nil {
		return wire.WriteJSON(s, jobsReply{Error: "bad request: " + err.Error()})
	}
	reply := func(r jobsReply, err error) error {
		if err != nil {
			r = jobsReply{Error: err.Error()}
		}
		return wire.WriteJSON(s, r)
	}
	// A session under a grant sees and handles only the jobs started
	// under its grant by its own identity, as with terminals: other jobs'
	// logs can hold the owner's (or another member's) secrets.
	owner := ""
	if g := grant.Of(c); g != nil {
		owner = grant.OwnerOf(g)
		if req.Op == "kill" || req.Op == "remove" {
			g.Log("jobs-"+req.Op, "job", req.Job, "jobs", strings.Join(req.Jobs, " "), "signal", req.Signal)
		}
		var err error
		switch req.Op {
		case "kill", "wait", "logs":
			req.Job, err = jobs.ownedRef(req.Job, owner)
		case "remove":
			req.Jobs, err = jobs.ownedRefs(req.Jobs, req.Exited, owner)
			req.Exited = false
		}
		if err != nil {
			if req.Op == "logs" {
				return (&frameWriter{w: s}).json(frameLogEnd, jobsReply{Error: err.Error()})
			}
			return reply(jobsReply{}, err)
		}
	}
	switch req.Op {
	case "list":
		js, err := jobs.list()
		if owner != "" {
			js = slices.DeleteFunc(js, func(j Job) bool { return j.Owner != owner })
		}
		return reply(jobsReply{Jobs: js}, err)
	case "kill":
		j, err := jobs.kill(req.Job, req.Signal)
		return reply(jobsReply{Job: j}, err)
	case "wait":
		ctx, cancel := withStream(ctx, s)
		defer cancel()
		j, err := jobs.wait(ctx, req.Job)
		return reply(jobsReply{Job: j}, err)
	case "remove":
		ids, err := jobs.remove(req.Jobs, req.Exited)
		return reply(jobsReply{Removed: ids}, err)
	case "logs":
		ctx, cancel := withStream(ctx, s)
		defer cancel()
		return jobs.logs(ctx, s, &req)
	}
	return reply(jobsReply{}, fmt.Errorf("unknown op %q", req.Op))
}

// withStream derives a context that also ends when the client abandons s.
func withStream(ctx context.Context, s peer.Stream) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	if gone := streamGone(s); gone != nil {
		go func() {
			select {
			case <-gone:
				cancel()
			case <-ctx.Done():
			}
		}()
	}
	return ctx, cancel
}

func newJobID() string {
	var b [4]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// spawn starts a background job; name is the grant it is spawned under
// and owner who spawned it (grant.OwnerOf), both "" for full access.
func (m *jobManager) spawn(req *spawnReq, name, owner string) (*Job, error) {
	if strings.TrimSpace(req.Command) == "" {
		return nil, errors.New("empty command")
	}
	if req.Name != "" {
		if err := validName(req.Name); err != nil {
			return nil, err
		}
	}
	if err := validEnv(req.Env); err != nil {
		return nil, err
	}
	dir, err := m.dir()
	if err != nil {
		return nil, err
	}
	cwd := resolveDir(req.Cwd)
	if st, err := os.Stat(cwd); err != nil {
		return nil, err
	} else if !st.IsDir() {
		return nil, errors.New(cwd + ": not a directory")
	}

	var id string
	var logf *os.File
	for range 8 {
		id = newJobID()
		logf, err = os.OpenFile(filepath.Join(dir, id+".log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o600)
		if !errors.Is(err, os.ErrExist) {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	defer logf.Close()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		os.Remove(logf.Name())
		return nil, err
	}
	defer devnull.Close()

	cmd := commandCmd(req.Command)
	cmd.Dir = cwd
	cmd.Env = buildEnv("", cwd, req.Env)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = devnull, logf, logf
	setDetached(cmd)
	if err := cmd.Start(); err != nil {
		os.Remove(logf.Name())
		return nil, err
	}
	meta := &jobMeta{
		Job: Job{
			ID: id, Name: req.Name, Command: req.Command, Cwd: cwd, PID: cmd.Process.Pid,
			State: JobRunning, Started: time.Now().UTC(), Log: logf.Name(), Grant: name, Owner: owner,
		},
		Ident: procStart(cmd.Process.Pid),
	}
	done := make(chan struct{})
	m.mu.Lock()
	err = m.saveLocked(dir, meta)
	m.children[id] = done
	known := *meta
	m.mem[id] = &known
	m.mu.Unlock()
	if err != nil {
		killGroup(meta.PID)
	}

	go func() {
		cmd.Wait()
		code, sig, name := exitInfo(cmd.ProcessState)
		now := time.Now().UTC()
		m.mu.Lock()
		// Record the exit unless the job was removed meanwhile. The
		// record must exist before the job stops counting as ours (the
		// delete below): if the file cannot be read or written right now,
		// the result is kept in memory instead.
		cur, lerr := m.loadLocked(dir, id)
		delete(m.mem, id)
		if lerr == nil {
			if k := m.kills[id]; k != "" {
				cur.KillSignal = k
			}
			if sig == 0 && cur.KillSignal != "" && !exitShowsSignal {
				code, sig, name = killedExit(cur.KillSignal)
			}
			cur.State, cur.ExitCode, cur.Signal, cur.SignalName, cur.Ended = JobExited, code, sig, name, &now
			if m.saveLocked(dir, cur) != nil {
				m.mem[id] = cur
			}
		}
		delete(m.children, id)
		delete(m.kills, id)
		m.mu.Unlock()
		close(done)
	}()
	if err != nil {
		return nil, err
	}
	j := meta.Job
	return &j, nil
}

func (m *jobManager) saveLocked(dir string, meta *jobMeta) error {
	b, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	p := filepath.Join(dir, meta.ID+".json")
	tmp := p + ".tmp"
	// Retry briefly: on Windows a file written a moment ago can still be
	// held open by a virus scanner or indexer, which makes replacing it
	// fail with a sharing violation.
	for attempt := 0; ; attempt++ {
		if err = os.WriteFile(tmp, b, 0o600); err == nil {
			err = os.Rename(tmp, p)
		}
		if err == nil || attempt == 20 {
			return err
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (m *jobManager) loadLocked(dir, id string) (*jobMeta, error) {
	b, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		if known, ok := m.mem[id]; ok && !errors.Is(err, os.ErrNotExist) {
			c := *known
			return &c, nil
		}
		return nil, err
	}
	var meta jobMeta
	if err := json.Unmarshal(b, &meta); err != nil {
		return nil, err
	}
	if meta.ID != id {
		return nil, fmt.Errorf("job %s: corrupt metadata", id)
	}
	return &meta, nil
}

// refreshLocked fills in the live fields of a loaded job.
func (m *jobManager) refreshLocked(meta *jobMeta) Job {
	j := meta.Job
	if j.State == JobRunning {
		if fin, ok := m.mem[j.ID]; ok && fin.State != JobRunning {
			// We reaped it but could not write the result down then; it is
			// authoritative, and worth another attempt at persisting.
			j = fin.Job
			if dir, err := m.dir(); err == nil && m.saveLocked(dir, fin) == nil {
				delete(m.mem, j.ID)
			}
		} else if _, ours := m.children[j.ID]; !ours && !procAlive(j.PID, meta.Ident) {
			j.State = JobUnknown
		}
	}
	if st, err := os.Stat(j.Log); err == nil {
		j.LogSize = st.Size()
	}
	return j
}

func (m *jobManager) allLocked(dir string) ([]*jobMeta, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []*jobMeta
	for _, e := range ents {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || validName(id) != nil {
			continue
		}
		if meta, err := m.loadLocked(dir, id); err == nil {
			out = append(out, meta)
		}
	}
	sort.Slice(out, func(i, k int) bool { return out[i].Started.Before(out[k].Started) })
	return out, nil
}

func (m *jobManager) list() ([]Job, error) {
	dir, err := m.dir()
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	all, err := m.allLocked(dir)
	if err != nil {
		return nil, err
	}
	out := make([]Job, 0, len(all))
	for _, meta := range all {
		out = append(out, m.refreshLocked(meta))
	}
	return out, nil
}

// ownedRef resolves ref (an ID or a unique name) among the jobs of owner
// (grant.OwnerOf) and returns the job's ID; a job of anyone else is "no
// job".
func (m *jobManager) ownedRef(ref, owner string) (string, error) {
	dir, err := m.dir()
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := validName(ref); err != nil {
		return "", fmt.Errorf("invalid job %q", ref)
	}
	if meta, err := m.loadLocked(dir, ref); err == nil && meta.Owner == owner {
		return meta.ID, nil
	}
	all, err := m.allLocked(dir)
	if err != nil {
		return "", err
	}
	found := ""
	for _, meta := range all {
		if meta.Owner == owner && meta.Name == ref {
			if found != "" {
				return "", fmt.Errorf("job name %q is ambiguous; use the job ID", ref)
			}
			found = meta.ID
		}
	}
	if found == "" {
		return "", fmt.Errorf("no job %q (a session under a grant sees only the jobs it started under that grant, from the same identity)", ref)
	}
	return found, nil
}

// ownedRefs is ownedRef for remove: the named jobs, and with exited every
// job of owner that is not running.
func (m *jobManager) ownedRefs(refs []string, exited bool, owner string) ([]string, error) {
	var out []string
	for _, ref := range refs {
		id, err := m.ownedRef(ref, owner)
		if err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if exited {
		js, err := m.list()
		if err != nil {
			return nil, err
		}
		for _, j := range js {
			if j.Owner == owner && j.State != JobRunning {
				out = append(out, j.ID)
			}
		}
	}
	return out, nil
}

// resolveLocked finds a job by ID or, failing that, by unique name.
func (m *jobManager) resolveLocked(dir, ref string) (*jobMeta, error) {
	if err := validName(ref); err != nil {
		return nil, fmt.Errorf("invalid job %q", ref)
	}
	if meta, err := m.loadLocked(dir, ref); err == nil {
		return meta, nil
	}
	all, err := m.allLocked(dir)
	if err != nil {
		return nil, err
	}
	var found *jobMeta
	for _, meta := range all {
		if meta.Name == ref {
			if found != nil {
				return nil, fmt.Errorf("job name %q is ambiguous; use the job ID", ref)
			}
			found = meta
		}
	}
	if found == nil {
		return nil, fmt.Errorf("no job %q", ref)
	}
	return found, nil
}

func (m *jobManager) get(ref string) (*jobMeta, Job, chan struct{}, error) {
	dir, err := m.dir()
	if err != nil {
		return nil, Job{}, nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	meta, err := m.resolveLocked(dir, ref)
	if err != nil {
		return nil, Job{}, nil, err
	}
	return meta, m.refreshLocked(meta), m.children[meta.ID], nil
}

func (m *jobManager) kill(ref, signal string) (*Job, error) {
	if signal == "" {
		signal = "TERM"
	}
	sig, err := parseSignal(signal)
	if err != nil {
		return nil, err
	}
	_, j, _, err := m.get(ref)
	if err != nil {
		return nil, err
	}
	if j.State != JobRunning {
		return nil, fmt.Errorf("job %s is not running (%s)", j.ID, j.State)
	}
	// Note the signal before sending it, so whoever reaps the job finds it.
	m.setKillSignal(j.ID, signalName(sig))
	if err := signalGroup(j.PID, sig); err != nil {
		m.setKillSignal(j.ID, "")
		return nil, fmt.Errorf("signal job %s: %w", j.ID, err)
	}
	return &j, nil
}

func (m *jobManager) setKillSignal(id, name string) {
	dir, err := m.dir()
	if err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ours := m.children[id]; ours {
		m.kills[id] = name // survives a failed write below
	}
	if cur, err := m.loadLocked(dir, id); err == nil && cur.State == JobRunning {
		cur.KillSignal = name
		m.saveLocked(dir, cur)
	}
}

// killedExit is the exit recorded for a job a client killed on a platform
// whose exit status does not show it: exit code 1 (what a terminated
// Windows process reports) with the signal that was requested.
func killedExit(signal string) (code, sig int, name string) {
	if s, err := parseSignal(signal); err == nil {
		sig = int(s)
	}
	return 1, sig, signal
}

// wait blocks until the job is no longer running.
func (m *jobManager) wait(ctx context.Context, ref string) (*Job, error) {
	meta, j, done, err := m.get(ref)
	if err != nil {
		return nil, err
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	} else {
		t := time.NewTicker(jobPoll)
		defer t.Stop()
		for j.State == JobRunning && procAlive(j.PID, meta.Ident) {
			select {
			case <-t.C:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	_, j, _, err = m.get(meta.ID)
	if err != nil {
		return nil, err
	}
	return &j, nil
}

// remove deletes finished jobs (metadata and log): the named ones, or with
// exited every job that is not running.
func (m *jobManager) remove(refs []string, exited bool) ([]string, error) {
	dir, err := m.dir()
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var targets []*jobMeta
	if exited {
		all, err := m.allLocked(dir)
		if err != nil {
			return nil, err
		}
		for _, meta := range all {
			if m.refreshLocked(meta).State != JobRunning {
				targets = append(targets, meta)
			}
		}
	}
	for _, ref := range refs {
		meta, err := m.resolveLocked(dir, ref)
		if err != nil {
			return nil, err
		}
		if m.refreshLocked(meta).State == JobRunning {
			return nil, fmt.Errorf("job %s is still running; kill it first", meta.ID)
		}
		targets = append(targets, meta)
	}
	removed := []string{}
	seen := map[string]bool{}
	for _, meta := range targets {
		if seen[meta.ID] {
			continue
		}
		seen[meta.ID] = true
		os.Remove(filepath.Join(dir, meta.ID+".log"))
		delete(m.mem, meta.ID)
		if err := os.Remove(filepath.Join(dir, meta.ID+".json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, err
		}
		removed = append(removed, meta.ID)
	}
	return removed, nil
}

// logs streams a job's log. Protocol: frameStdout chunks, then one
// frameLogEnd carrying a jobsReply with the job's state at that point (or
// an error). With Follow the stream continues until the job has ended and
// its output has been drained.
func (m *jobManager) logs(ctx context.Context, s peer.Stream, req *jobsReq) error {
	fw := &frameWriter{w: s}
	meta, j, done, err := m.get(req.Job)
	if err != nil {
		return fw.json(frameLogEnd, jobsReply{Error: err.Error()})
	}
	f, err := os.Open(j.Log)
	if err != nil {
		return fw.json(frameLogEnd, jobsReply{Error: err.Error()})
	}
	defer f.Close()
	if req.Tail > 0 {
		off, err := tailOffset(f, req.Tail)
		if err == nil {
			_, err = f.Seek(off, io.SeekStart)
		}
		if err != nil {
			return fw.json(frameLogEnd, jobsReply{Error: err.Error()})
		}
	}

	t := time.NewTicker(jobPoll)
	defer t.Stop()
	final := !req.Follow
	buf := make([]byte, ioChunk)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if err := fw.frame(frameStdout, buf[:n]); err != nil {
				return err
			}
		}
		if err == nil {
			continue
		}
		if err != io.EOF {
			return fw.json(frameLogEnd, jobsReply{Error: err.Error()})
		}
		if final {
			break
		}
		_, cur, _, gerr := m.get(meta.ID)
		if gerr != nil || cur.State != JobRunning {
			// One more pass picks up output written just before the end.
			final = true
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-done:
			done = nil
		case <-t.C:
		}
	}
	_, j, _, err = m.get(meta.ID)
	if err != nil {
		return fw.json(frameLogEnd, jobsReply{Error: err.Error()})
	}
	return fw.json(frameLogEnd, jobsReply{Job: &j})
}

// tailOffset returns the offset at which the last n lines of f start.
func tailOffset(f *os.File, n int) (int64, error) {
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	end := st.Size()
	pos := end
	buf := make([]byte, 32<<10)
	count := 0
	for pos > 0 {
		k := int64(len(buf))
		if pos < k {
			k = pos
		}
		pos -= k
		if _, err := f.ReadAt(buf[:k], pos); err != nil && err != io.EOF {
			return 0, err
		}
		for i := k - 1; i >= 0; i-- {
			// A newline ending the file terminates the last line rather
			// than starting an empty one.
			if buf[i] != '\n' || pos+i == end-1 {
				continue
			}
			count++
			if count == n {
				return pos + i + 1, nil
			}
		}
	}
	return 0, nil
}
