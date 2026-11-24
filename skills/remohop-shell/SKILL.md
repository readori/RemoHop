---
name: remohop-shell
description: Run commands on a remote machine by its 9-digit RemoHop ID with the `remohop` CLI - probe the environment, read and filter logs, run long jobs in the background and check on them later. Use when the user gives you a RemoHop ID (like 123456789) or an alias, or asks you to look at, debug, build on or run something on another computer, server, VM or home/office machine that has `remohop host` running. No SSH, VPN or open ports needed.
---

# RemoHop remote shell

`remohop <ID> ...` runs on the remote machine as the user that runs `remohop host` there (full privileges). `<ID>` is a 9-digit ID, a saved alias, or `host:port`. The machine must already be paired (`remohop <ID> pair`, done once by the human) - see `skills/README.md`. If a command fails with exit 255 and a credentials or pairing message, stop and ask the user to run `remohop <ID> pair`; never try to guess passwords. If the error names `remohop config feature NAME on`, the host's owner has switched that feature off: report it, do not retry.

(With the MCP server: `remohop_info`, `remohop_exec`, `remohop_spawn`, `remohop_jobs`, `remohop_job_logs`, `remohop_job_wait`, `remohop_job_kill`.)

## 1. Probe first

```bash
remohop <ID> info            # OS, kernel, CPU, memory, disk, user, cwd, display yes/no
remohop <ID> info --json     # same, parseable
remohop <ID> ping            # is it reachable, and over which path
```

## 2. Run a command

```bash
remohop <ID> shell -c 'uname -a; df -h /'
remohop <ID> shell -c 'make test' --cwd src/app --env CI=1 --timeout 10m
remohop <ID> shell -c 'cat > /tmp/x.sh' < x.sh            # local file -> remote stdin
remohop <ID> shell -c 'tar czf - /etc/nginx' > nginx.tgz   # remote stdout -> local file
remohop <ID> shell -n -c 'systemctl is-active nginx'       # -n: do not read stdin
```

- **The command language is the remote host's shell**: POSIX sh on Linux and macOS, **PowerShell on Windows** (`Get-ChildItem`, `$env:NAME`, `;` between statements, stdin via `$input`; `cat` does not read stdin there). `remohop <ID> info` shows the OS - check before writing commands.
- The command goes through the remote login shell (`$SHELL -c`); wrap it in **single quotes** locally so your own shell does not expand `$VARS`, globs or `&&` first. For a script with awkward quoting, pipe it: `remohop <ID> shell -c 'bash -s' < script.sh`.
- `--cwd` is relative to the remote home unless absolute. `--env K=V` is repeatable.
- stdout and stderr arrive on your stdout and stderr; **the exit code is the remote command's** (`128+n` if killed by signal n). Special codes: **124** = `--timeout` expired (remote process group killed), **130** = interrupted locally, **255** = `remohop` itself failed (could not connect, authenticate, or start the command). Distinguish 255 from a failing remote command before debugging the wrong side.
- Always set `--timeout` for anything that might hang, and use `-n` when nothing should be read from stdin (otherwise a command that reads stdin can wait on yours).
- `-t` allocates a PTY (output merged); only needed for programs that insist on a terminal.

## 3. Logs and big output: filter on the remote side

Do not pull whole logs into your context. Reduce first:

```bash
remohop <ID> shell -c 'journalctl -u myapp --since "10 min ago" -p warning --no-pager | tail -n 80'
remohop <ID> shell -c 'grep -c ERROR /var/log/app.log; grep -n ERROR /var/log/app.log | tail -n 20'
remohop <ID> shell -c 'tail -n 200 /var/log/app.log | grep -v healthcheck'
remohop <ID> shell -c 'wc -l /var/log/app.log; ls -lh /var/log/app*'   # size up before reading
```

Typical loop: count/size -> narrow by time or pattern -> read a small window around the hit (`grep -n -B5 -A20 'Traceback' ... | head -n 100`). Never run `cat` on an unbounded file. For files you want locally, see the `remohop-files` skill.

## 4. Long-running work: spawn, don't wait

A foreground `shell -c` dies with your connection and blocks you. For builds, test suites, servers, downloads, or anything over about a minute, start a background job. Jobs survive disconnects and restarts of the `remohop host` daemon; output goes to a log on the host.

```bash
remohop <ID> spawn --name build --cwd src/app make -j8     # prints the job ID; flags BEFORE the command
remohop <ID> jobs                                          # list: running / exited(CODE) / unknown
remohop <ID> logs --tail 50 build                          # last 50 lines so far
remohop <ID> logs -f build                                 # follow until it ends; exit code = job's
remohop <ID> jobs wait build && echo built                 # block until done; exit code = job's
remohop <ID> kill build                                    # SIGTERM to the whole process group
remohop <ID> kill -s KILL build                            # force
remohop <ID> jobs rm --exited                              # clean up finished jobs and logs
```

`logs -f` and `jobs wait` block until the job ends: wrap them in your host's timeout, or poll with `logs --tail N` instead. Jobs have stdin from /dev/null, so they cannot prompt. Use `--name` so you can refer to the job without tracking IDs.

## 5. When not to use interactive `remohop <ID> shell`

`remohop <ID> shell` with no `-c` opens a persistent terminal (like tmux). It needs a TTY and a human; **do not run it from an agent shell tool** - it will hang or garble. Use `shell -c`, or `shell -t -c 'cmd'` if a program needs a PTY. Sessions: `remohop <ID> shell ls`, `remohop <ID> shell kill NAME`.

## Habits

- Check `info` once so you write commands for the right shell (PowerShell on Windows hosts). The same applies to `spawn`.
- Prefer read-only commands to explore; state what you are about to change on the remote machine before changing it.
- Quote carefully, set `--timeout`, bound output, and report the exit code you saw.
