package shell

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/wire"
)

// SpawnRequest starts a background job.
type SpawnRequest struct {
	// Command runs through the host user's shell.
	Command string
	// Name is an optional label; jobs can be referred to by unique name.
	Name string
	// Cwd and Env work as in ExecRequest.
	Cwd string
	Env []string
}

// Spawn starts a detached background job on the host and returns it.
func Spawn(ctx context.Context, conn *peer.Conn, req SpawnRequest) (*Job, error) {
	if req.Command == "" {
		return nil, errors.New("shell: empty command")
	}
	rep, err := jobsCall(ctx, conn, svcSpawn, spawnReq{Command: req.Command, Name: req.Name, Cwd: req.Cwd, Env: req.Env})
	if err != nil {
		return nil, err
	}
	return checkJob(rep.Job)
}

// Jobs lists the host's background jobs, oldest first.
func Jobs(ctx context.Context, conn *peer.Conn) ([]Job, error) {
	rep, err := jobsCall(ctx, conn, svcJobs, jobsReq{Op: "list"})
	if err != nil {
		return nil, err
	}
	if rep.Jobs == nil {
		rep.Jobs = []Job{}
	}
	for i := range rep.Jobs {
		if _, err := checkJob(&rep.Jobs[i]); err != nil {
			return nil, err
		}
	}
	return rep.Jobs, nil
}

// Kill sends signal (a name such as "TERM" or "KILL", or a number; ""
// means TERM) to a running job's whole process group. job is an ID or a
// unique name.
func Kill(ctx context.Context, conn *peer.Conn, job, signal string) (*Job, error) {
	rep, err := jobsCall(ctx, conn, svcJobs, jobsReq{Op: "kill", Job: job, Signal: signal})
	if err != nil {
		return nil, err
	}
	return checkJob(rep.Job)
}

// Wait blocks until the job has ended and returns its final state.
func Wait(ctx context.Context, conn *peer.Conn, job string) (*Job, error) {
	rep, err := jobsCall(ctx, conn, svcJobs, jobsReq{Op: "wait", Job: job})
	if err != nil {
		return nil, err
	}
	return checkJob(rep.Job)
}

// RemoveRequest selects finished jobs to delete along with their logs.
type RemoveRequest struct {
	Jobs []string // IDs or unique names; running jobs are refused
	// Exited also selects every job that is not running.
	Exited bool
}

// RemoveJobs deletes finished jobs and returns the removed IDs.
func RemoveJobs(ctx context.Context, conn *peer.Conn, req RemoveRequest) ([]string, error) {
	rep, err := jobsCall(ctx, conn, svcJobs, jobsReq{Op: "remove", Jobs: req.Jobs, Exited: req.Exited})
	if err != nil {
		return nil, err
	}
	for _, id := range rep.Removed {
		if validName(id) != nil {
			return nil, errBadReply
		}
	}
	return rep.Removed, nil
}

var errBadReply = errors.New("shell: malformed reply from host")

// checkJob vets a job the host sent back. The client never trusts the host:
// IDs and names end up in hints the user pastes into a shell, so they must
// be plain names, and a missing job is an error rather than a nil pointer.
func checkJob(j *Job) (*Job, error) {
	if j == nil || validName(j.ID) != nil || (j.Name != "" && validName(j.Name) != nil) {
		return nil, errBadReply
	}
	return j, nil
}

func jobsCall(ctx context.Context, conn *peer.Conn, service string, req any) (*jobsReply, error) {
	s, err := conn.Open(ctx, service, req)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	stop := context.AfterFunc(ctx, s.Reset)
	defer stop()
	var rep jobsReply
	if err := wire.ReadJSON(s, &rep); err != nil {
		if ce := ctx.Err(); ce != nil {
			return nil, ce
		}
		return nil, fmt.Errorf("shell: %s: %w", service, err)
	}
	if rep.Error != "" {
		return nil, errors.New("remote: " + printable(rep.Error))
	}
	return &rep, nil
}

// LogsRequest selects a job's log.
type LogsRequest struct {
	Job string
	// Follow keeps streaming new output until the job ends.
	Follow bool
	// Tail starts at the last Tail lines (0 = from the beginning).
	Tail int
}

// LogReader streams a job's log. Read returns io.EOF at the end of the log
// (with Follow: once the job has ended and all its output was read); Job
// then reports the job's state.
type LogReader struct {
	ctx  context.Context
	s    peer.Stream
	stop func() bool
	buf  []byte
	rest []byte
	job  *Job
	err  error
}

// Logs opens a job's log on the host. Close the reader when done.
func Logs(ctx context.Context, conn *peer.Conn, req LogsRequest) (*LogReader, error) {
	if req.Tail < 0 {
		return nil, errors.New("shell: negative tail")
	}
	s, err := conn.Open(ctx, svcJobs, jobsReq{Op: "logs", Job: req.Job, Follow: req.Follow, Tail: req.Tail})
	if err != nil {
		return nil, err
	}
	return &LogReader{ctx: ctx, s: s, stop: context.AfterFunc(ctx, s.Reset), buf: make([]byte, maxFrame)}, nil
}

func (r *LogReader) Read(p []byte) (int, error) {
	for len(r.rest) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		typ, data, err := readFrame(r.s, r.buf)
		if err != nil {
			if ce := r.ctx.Err(); ce != nil {
				r.err = ce
				continue
			}
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			r.err = fmt.Errorf("shell: logs: %w", err)
			continue
		}
		switch typ {
		case frameStdout:
			r.rest = data
		case frameLogEnd:
			var rep jobsReply
			switch {
			case json.Unmarshal(data, &rep) != nil:
				r.err = errors.New("shell: logs: bad end message")
			case rep.Error != "":
				r.err = errors.New("remote: " + printable(rep.Error))
			default:
				if j, err := checkJob(rep.Job); err != nil {
					r.err = err
				} else {
					r.job, r.err = j, io.EOF
				}
			}
		}
	}
	n := copy(p, r.rest)
	r.rest = r.rest[n:]
	return n, nil
}

// Job is the job's state as of the end of the log; nil before Read has
// returned io.EOF.
func (r *LogReader) Job() *Job { return r.job }

// Close ends the stream.
func (r *LogReader) Close() error {
	r.stop()
	return r.s.Close()
}
