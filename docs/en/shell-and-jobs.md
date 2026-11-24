# Shell and background jobs

[中文](../zh/shell-and-jobs.md)

Every remote command has the form `remohop <ID> <command>`. `dev` is the example alias from `remohop <ID> pair --name dev` (see [Quick start](quick-start.md)). Run `remohop help <command>` for the full syntax.

An interactive shell is persistent: it keeps running on the host after you disconnect and reattaches with the output you missed.

```bash
remohop dev shell
```

Press Enter, then `~.` to detach. The session stays on the host for 30 minutes by default (the host's `term-keep` setting). Reattach with `remohop dev shell`, or start another session with `remohop dev shell -s build --new`.

Run a single command, or a background job that keeps running after you disconnect:

```bash
remohop dev shell -c 'make test' --cwd src/app --env CI=1 --timeout 10m
remohop dev spawn --name build --cwd src/app make -j8
remohop dev logs -f build
remohop dev jobs
remohop dev kill build
```

On a Windows host the shell is PowerShell, so commands are PowerShell syntax there.
