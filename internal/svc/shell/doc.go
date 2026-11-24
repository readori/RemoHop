// Package shell is the RemoHop remote shell service: interactive persistent
// terminal sessions (`remohop <ID> shell`), non-interactive commands
// (`remohop <ID> shell -c`) and detached background jobs (`remohop <ID> spawn`,
// `jobs`, `logs`, `kill`). It owns the host handlers, the Go client API used
// by the CLI and the MCP server, and the CLI commands.
//
// # Wire protocol
//
// Four host services, each request carried in the stream header:
//
//   - "exec": one command. Client sends input frames ('i'), one input-EOF
//     frame ('e') and resize frames ('r', rows/cols as two big-endian
//     uint16); host sends stdout ('o') and stderr ('E') frames, then one
//     exit frame ('x', JSON). The client keeps its direction open until the
//     exit frame: the host treats its end, or a stream reset, as the client
//     going away and kills the process group.
//   - "shell": interactive sessions, op "attach" | "list" | "kill". An
//     attach answers with an attached frame ('a', JSON), then output frames
//     (replay of recent output first), and finally an exit frame (the shell
//     ended) or a detached frame ('d': another client took over). The client
//     sends input and resize frames, and detaches by closing the stream.
//     "list" and "kill" answer with one JSON message.
//   - "spawn": start a background job; one JSON reply.
//   - "jobs": op "list" | "kill" | "wait" | "remove" answer with one JSON
//     message; "logs" streams output frames and ends with a log-end frame
//     ('z', JSON with the job's state).
//
// Frames use the internal/wire binary framing; this package refuses frames
// above 256 KiB from either side.
//
// # Sessions
//
// Sessions belong to the host process, not to a connection, and survive
// their client for KeepAlive. One client is attached at a time; a second
// attach takes the session over and the previous client is detached.
//
// # Jobs
//
// Jobs run in their own session with stdin from /dev/null and stdout and
// stderr appended to <config dir>/jobs/<id>.log, with metadata in
// <id>.json, so they outlive both the client and the host daemon. Logs are
// not rotated; finished jobs are deleted with RemoveJobs (`jobs rm`).
//
// # Platforms
//
// Linux and macOS share the unix code path (creack/pty, process groups,
// waitid/kqueue to observe exits without reaping). Windows hosts create
// processes directly with CreateProcess: each one suspended, put in its own
// kill-on-close job object, then resumed, so a kill or timeout ends the
// whole tree. Terminals are ConPTY pseudo consoles; commands and jobs run
// through PowerShell (-EncodedCommand, UTF-8 output) or, without it,
// cmd.exe. Background jobs are killed with taskkill /T, the only way to
// reach a tree after the host restarted. Windows clients put the console in
// raw mode with virtual terminal input and output.
package shell
