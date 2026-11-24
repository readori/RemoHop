package mcp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/readori/RemoHop/internal/auth"
	"github.com/readori/RemoHop/internal/hostd"
	"github.com/readori/RemoHop/internal/peer"
)

type limitsIn struct {
	Target string `json:"target,omitempty" jsonschema:"Remote machine: 9-digit RemoHop ID, saved alias or host:port. Optional if the server has a default target."`
}

type limitsOut struct {
	Grant       string   `json:"grant,omitempty" jsonschema:"Name of the grant this computer's access to the machine stands on; empty means full access (limited only by the host's feature switches)."`
	Expires     string   `json:"expires,omitempty" jsonschema:"When the grant ends (RFC 3339); the session ends with it."`
	Allowed     []string `json:"allowed" jsonschema:"What may be done, in the grant vocabulary (shell:run-only, files:read under ..., screenshot, ...)."`
	Denied      []string `json:"denied,omitempty" jsonschema:"Capabilities the grant does not include. Do not try them; ask the person if you need one."`
	Shell       string   `json:"shell" jsonschema:"none, run-only (remohop_exec/remohop_spawn, no terminal) or full."`
	Commands    []string `json:"commands,omitempty" jsonschema:"If set, the only commands that may run: each entry is a program with optional leading arguments, and a command must start with one of them. Shell operators (; | & > < $ backtick) are refused."`
	ReadAll     bool     `json:"read_all,omitempty" jsonschema:"Files may be read anywhere."`
	WriteAll    bool     `json:"write_all,omitempty" jsonschema:"Files may be written anywhere."`
	ReadRoots   []string `json:"read_roots,omitempty" jsonschema:"Directories below which files may be read."`
	WriteRoots  []string `json:"write_roots,omitempty" jsonschema:"Directories below which files may be read and written."`
	Approve     string   `json:"approve,omitempty" jsonschema:"each: the host's owner approves every command and file change (calls may wait up to about 30 s and fail if nobody answers); first: only the first one of the session; none."`
	Recorded    bool     `json:"recorded,omitempty" jsonschema:"Command and terminal output is recorded on the host for the owner to review."`
	FeaturesOff []string `json:"features_off,omitempty" jsonschema:"Features the host's owner switched off for everybody."`
	ReadOnly    bool     `json:"mcp_read_only,omitempty" jsonschema:"This MCP server runs in read-only mode: tools that change anything are not offered."`
}

func (s *Server) registerLimits() {
	addTool(s, "remohop_limits", "Show what you may do on a remote machine",
		"Ask the remote machine what this computer's access to it allows. The host enforces it: a person can hand a machine to an agent with a grant that limits which tools work "+
			"(shell commands, an allow-list of commands, file reads or writes below certain directories, screenshots, ...), until when, and whether they approve each action. "+
			"Call it first on a machine you have not used yet, and whenever a tool reports that something \"is not allowed by grant\"; then stay within the limits instead of trying workarounds. "+
			"Changes nothing.",
		readOnly(), s.limits)
}

func (s *Server) limits(ctx context.Context, in limitsIn) (*reply[limitsOut], error) {
	var l *hostd.Limits
	err := s.withConn(ctx, in.Target, true, func(ctx context.Context, c *peer.Conn) error {
		var err error
		l, err = hostd.GetLimits(ctx, c)
		if l != nil {
			return nil // a grant that is no longer usable is still described
		}
		var re *peer.RemoteError
		if errors.As(err, &re) && re.Message == peer.ErrUnknownService {
			return errors.New("the remote machine runs an older RemoHop that cannot report its limits; it grants this computer full access unless its owner switched features off")
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	out := limitsOut{Grant: l.Grant, Allowed: l.Allowed, Denied: l.Denied, Shell: l.Shell, Commands: l.Commands,
		ReadAll: l.ReadAll, WriteAll: l.WriteAll, ReadRoots: l.ReadRoots, WriteRoots: l.WriteRoots,
		Approve: l.Approve, Recorded: l.Recorded, FeaturesOff: l.FeaturesOff, ReadOnly: s.opts.ReadOnly}
	if l.Expires != nil {
		out.Expires = l.Expires.UTC().Format(time.RFC3339)
	}
	if out.Allowed == nil {
		out.Allowed = []string{}
	}
	cleanStrings(&out)
	for _, list := range [][]string{out.Allowed, out.Denied, out.Commands, out.ReadRoots, out.WriteRoots, out.FeaturesOff} {
		for i := range list {
			list[i] = auth.CleanText(list[i], 500)
		}
	}
	var b strings.Builder
	if out.Grant == "" {
		b.WriteString("Full access: this computer may do everything the host's user can")
		if len(out.FeaturesOff) > 0 {
			fmt.Fprintf(&b, ", except what the host's owner switched off: %s", strings.Join(out.FeaturesOff, ", "))
		}
		b.WriteString(".\n")
	} else {
		fmt.Fprintf(&b, "Access through grant %s", out.Grant)
		if out.Expires != "" {
			fmt.Fprintf(&b, ", valid until %s", out.Expires)
		}
		b.WriteString(".\n")
		if l.Error != "" {
			fmt.Fprintf(&b, "The grant cannot be used now: %s. Tell the person; do not retry.\n", clean(auth.CleanText(l.Error, 300)))
		}
		for _, a := range out.Allowed {
			fmt.Fprintf(&b, "Allowed: %s\n", a)
		}
		if len(out.Denied) > 0 {
			fmt.Fprintf(&b, "Not allowed: %s\n", strings.Join(out.Denied, ", "))
		}
		switch out.Approve {
		case "each":
			b.WriteString("The host's owner approves every command and file change: calls may wait for them and fail if they deny or do not answer.\n")
		case "first":
			b.WriteString("The host's owner approves the first command or file change of the session.\n")
		}
		if out.Recorded {
			b.WriteString("Command and terminal output is recorded on the host.\n")
		}
		if len(out.FeaturesOff) > 0 {
			fmt.Fprintf(&b, "Also switched off on the host: %s\n", strings.Join(out.FeaturesOff, ", "))
		}
	}
	if s.opts.ReadOnly {
		b.WriteString("This MCP server is in read-only mode: tools that change anything are not offered.\n")
	}
	return textReply(out, b.String()), nil
}

// grantHint adds guidance to a tool error that is a grant's refusal, so the
// model stops instead of looking for a way around the limit.
func grantHint(msg string) string {
	switch {
	case strings.Contains(msg, "is not allowed by grant "):
		return msg + "\nThis limit was set by the machine's owner and is enforced by the host. Do not try to work around it; " +
			"call remohop_limits to see what is allowed, or ask the person to widen the grant."
	case strings.Contains(msg, "the host's owner denied"), strings.Contains(msg, "nobody approved"):
		return msg + "\nThe machine's owner reviews actions under this grant. Do not repeat the action unless the person asks you to."
	case strings.Contains(msg, "expired at"), strings.Contains(msg, "is suspended"), strings.Contains(msg, "was revoked"):
		if strings.Contains(msg, "grant ") {
			return msg + "\nThe grant giving this computer access has ended. Tell the person; retrying will not help."
		}
	}
	return msg
}
